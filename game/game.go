package game

import (
	"encoding/json"
	"fmt"
	"image/color"
	"log"
	"os"
	"time"

	"github.com/al-pi314/gogo"
	"github.com/al-pi314/gogo/player"
	"github.com/hajimehoshi/ebiten/v2"
	"github.com/hajimehoshi/ebiten/v2/ebitenutil"
	"github.com/hajimehoshi/ebiten/v2/text"
	"github.com/pkg/errors"
	"golang.org/x/image/font/basicfont"
)

type Cordinate struct {
	X int
	Y int
}

type Game struct {
	SaveFileName string
	saveFile     *os.File
	Dymension    int
	SquareSize   int
	BorderSize   int
	WhitePlayer  player.Player
	BlackPlayer  player.Player
	MoveDelay    *int

	active      bool
	whiteToMove bool

	delay_lock bool
	locked     Cordinate

	replayMoves   [][2]*int
	replayMoveIdx int
	isReplay      bool

	gameState *GameState
}

type GameState = gogo.GameState

func NewGame(g Game) *Game {
	g.gameState = &GameState{
		Board: make([][]*bool, g.Dymension),
		Moves: [][2]*int{},
	}
	for y := range g.gameState.Board {
		g.gameState.Board[y] = make([]*bool, g.Dymension)
	}
	g.locked = Cordinate{-1, -1}
	g.active = true

	return &g
}

// ------------------------------------ Helper Functions ------------------------------------ \\
type GameSave struct {
	Time  *time.Time
	Moves [][2]*int
}

func (g *Game) Save() {
	if g.saveFile == nil {
		g.OpenOutputFile(g.SaveFileName)
	}
	defer func() {
		g.saveFile.Close()
		g.saveFile = nil
	}()

	now := time.Now()
	bytes, err := json.Marshal(GameSave{
		Time:  &now,
		Moves: g.gameState.Moves,
	})
	if err != nil {
		log.Fatal(errors.Wrap(err, "could not marshal game"))
	}

	n, err := g.saveFile.Write(bytes)
	if err != nil || n != len(bytes) {
		log.Fatal(errors.Wrap(err, fmt.Sprintf("writting error or the write was incomplete (attempted to write %d bytes, written %d bytes)", len(bytes), n)))
	}
	fmt.Println("saved game!")
}

func (g *Game) OpenOutputFile(filePath string) {
	var err error
	g.saveFile, err = os.OpenFile(filePath, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0755)
	if err != nil {
		log.Print(errors.Wrap(err, fmt.Sprintf("failed to open game output file %q", filePath)))
	} else {
		g.SaveFileName = filePath
	}
}

func (g *Game) Size() (int, int) {
	side := g.Dymension*(g.SquareSize+g.BorderSize) + g.BorderSize
	return side, side
}

// drawSquare draws a square on the image.
func (g *Game) drawSquare(screen *ebiten.Image, x1, y1, x2, y2 int, clr color.Color) {
	for x := x1; x <= x2; x++ {
		for y := y1; y <= y2; y++ {
			screen.Set(x, y, clr)
		}
	}
}

func (g *Game) pieceAt(x, y int) *bool {
	if y >= len(g.gameState.Board) || y < 0 || x >= len(g.gameState.Board[y]) || x < 0 {
		return nil
	}
	return g.gameState.Board[y][x]
}

func (g *Game) hasRoom(x, y int, white bool, checked map[Cordinate]bool, group *[]Cordinate) bool {
	c := Cordinate{x, y}
	if chk, ok := checked[c]; ok && chk {
		return false
	}
	checked[c] = true

	if y >= len(g.gameState.Board) || y < 0 || x >= len(g.gameState.Board[y]) || x < 0 {
		return false
	}

	if g.gameState.Board[y][x] == nil {
		return true
	}

	if *g.gameState.Board[y][x] != white {
		return false
	}

	// record group
	if group != nil {
		*group = append(*group, c)
	}

	// execute all checks
	results := []bool{g.hasRoom(x-1, y, white, checked, group), g.hasRoom(x+1, y, white, checked, group), g.hasRoom(x, y-1, white, checked, group), g.hasRoom(x, y+1, white, checked, group)}
	for _, r := range results {
		if r {
			return true
		}
	}
	return false
}

func (g *Game) findGroup(x, y int, white bool) []Cordinate {
	pieceColor := g.pieceAt(x, y)
	if pieceColor == nil || *pieceColor != white {
		return nil
	}

	group := []Cordinate{}
	if g.hasRoom(x, y, white, map[Cordinate]bool{}, &group) {
		return nil
	}

	return group
}

func updateCtr(whitePtr, blackPtr *int, iswhite bool, cnt int) {
	if iswhite {
		blackPtr = whitePtr
	}
	*blackPtr += cnt
}

func (g *Game) asignTeritory(x, y int, checked map[Cordinate]bool) (bool, *bool, int) {
	c := Cordinate{x, y}
	if chk, ok := checked[c]; ok && chk {
		return true, nil, 0
	}
	checked[c] = true

	if y < 0 || y >= len(g.gameState.Board) || x < 0 || x >= len(g.gameState.Board[y]) {
		return true, nil, 0
	}
	if g.gameState.Board[y][x] != nil {
		return true, g.gameState.Board[y][x], 0
	}

	prev_uniform, prev_owner, prev_cnt := g.asignTeritory(x, y-1, checked)
	for _, c := range []Cordinate{{x, y + 1}, {x - 1, y}, {x + 1, y}} {
		curr_uniform, curr_owner, curr_cnt := g.asignTeritory(c.X, c.Y, checked)
		// previous teritory is not uniform - does not belong to just one player
		if !prev_uniform {
			return false, nil, 0
		}

		// teritories ownership missmatch
		if prev_owner != nil && curr_owner != nil && *prev_owner != *curr_owner {
			return false, nil, 0
		}

		prev_uniform = curr_uniform
		if curr_owner != nil {
			prev_owner = curr_owner
		}
		prev_cnt += curr_cnt
	}
	return prev_uniform, prev_owner, 1 + prev_cnt
}

// ------------------------------------ ----------------- ------------------------------------ \\

// -------------------------------------- Game Functions ------------------------------------- \\
func (g *Game) ReplayFromFile(gameFile string) {
	raw, err := os.ReadFile(gameFile)
	if err != nil {
		log.Fatal(errors.Wrap(err, "failed to open game file"))
	}

	gameSave := GameSave{}
	if err := json.Unmarshal(raw, &gameSave); err != nil {
		log.Fatal(errors.Wrap(err, "failed to load game save file"))
	}

	fmt.Printf("...replaying game save from %s\n", gameSave.Time.String())
	g.replayMoves = gameSave.Moves
	g.replayMoveIdx = 0
	g.isReplay = true
	fmt.Printf("...game lasted %d moves\n", len(g.replayMoves))
}

func (g *Game) placePiece(x, y int, white bool) bool {
	// out of bounds or already occupied spaces are invalid
	if y >= len(g.gameState.Board) || y < 0 || x >= len(g.gameState.Board[y]) || x < 0 || g.gameState.Board[y][x] != nil {
		return false
	}
	// place piece
	g.gameState.Board[y][x] = &white
	updateCtr(&g.gameState.WhiteStones, &g.gameState.BlackStones, white, 1)

	// check for opponent group eliminations
	if g.caputreOpponent(x, y, white) {
		return true
	}

	// would be eliminated when placed
	hasRoom := g.hasRoom(x, y, white, map[Cordinate]bool{}, nil)
	if !hasRoom {
		g.gameState.Board[y][x] = nil
		updateCtr(&g.gameState.WhiteStones, &g.gameState.BlackStones, white, -1)
		return false
	}

	return true
}

func (g *Game) caputreOpponent(x, y int, white bool) bool {
	toRemove := []Cordinate{}
	if g := g.findGroup(x-1, y, !white); g != nil {
		toRemove = append(toRemove, g...)
	}
	if g := g.findGroup(x+1, y, !white); g != nil {
		toRemove = append(toRemove, g...)
	}
	if g := g.findGroup(x, y-1, !white); g != nil {
		toRemove = append(toRemove, g...)
	}
	if g := g.findGroup(x, y+1, !white); g != nil {
		toRemove = append(toRemove, g...)
	}

	// ko rule
	if len(toRemove) == 1 {
		if toRemove[0].X == g.locked.X && toRemove[0].Y == g.locked.Y {
			return false
		}
		g.delay_lock = true
		g.locked.X = x
		g.locked.Y = y
	}

	for _, c := range toRemove {
		g.gameState.Board[c.Y][c.X] = nil
	}

	updateCtr(&g.gameState.WhiteStones, &g.gameState.BlackStones, !white, -len(toRemove))
	updateCtr(&g.gameState.WhiteStonesCaptured, &g.gameState.BlackStonesCaptured, !white, len(toRemove))
	return len(toRemove) != 0
}

// FullMoves returns number of moves and number of moves made by each player
func (g *Game) FullMoves() (int, int, int) {
	return g.gameState.MovesCount, g.gameState.WhiteMoves, g.gameState.BlackMoves
}

// Moves returns number of moves palyed by players.
func (g *Game) Moves() int {
	return g.gameState.MovesCount
}

// FullScore calculates game score and score of both players.
func (g *Game) FullScore() (float64, float64, float64) {
	checked := map[Cordinate]bool{}
	score := 0.5 + float64(g.gameState.WhiteStones) - float64(g.gameState.WhiteStonesCaptured) - float64(g.gameState.BlackStones) + float64(g.gameState.BlackStonesCaptured)
	white := float64(g.gameState.BlackStonesCaptured)
	black := float64(g.gameState.WhiteStonesCaptured)
	for y := range g.gameState.Board {
		for x := range g.gameState.Board[y] {
			if chk, ok := checked[Cordinate{x, y}]; g.gameState.Board[y][x] != nil || (ok && chk) {
				continue
			}
			uniform, owner, size := g.asignTeritory(x, y, checked)
			if uniform && owner != nil {
				sign := -1
				if *owner {
					white += float64(size)
					sign = 1
				} else {
					black += float64(size)
					sign = -1
				}
				score += float64(sign * size)
			}
		}
	}
	return score, white, black
}

// Score returns game score.
func (g *Game) Score() float64 {
	gameScore, _, _ := g.FullScore()
	return gameScore
}

// -------------------------------------- -------------- ------------------------------------- \\

// --------------------------- Functions required by ebiten engine --------------------------- \\
func (g *Game) Update() error {
	if !g.active {
		return errors.New("game finished")
	}

	player := g.WhitePlayer
	opponent := g.BlackPlayer
	if !g.whiteToMove {
		player = g.BlackPlayer
		opponent = g.WhitePlayer
	}

	var skip bool
	var x, y *int
	if !g.isReplay {
		skip, x, y = player.Place(g.gameState)
	} else {
		if g.replayMoves == nil || g.replayMoveIdx >= len(g.replayMoves) {
			g.active = false
			return nil
		}

		cord := g.replayMoves[g.replayMoveIdx]
		x = cord[0]
		y = cord[1]
		skip = (x == nil && y == nil)
		g.replayMoveIdx++
	}

	if skip || ((x != nil && y != nil) && g.placePiece(*x, *y, g.whiteToMove)) {
		// save move
		g.gameState.MovesCount++
		g.gameState.Moves = append(g.gameState.Moves, [2]*int{x, y})

		// consequitive skips end the game
		if skip && g.gameState.OpponentSkipped {
			g.active = false
		}
		g.gameState.OpponentSkipped = false
		if skip {
			g.gameState.OpponentSkipped = true
			if g.whiteToMove {
				g.gameState.WhiteMoves += 1
			} else {
				g.gameState.BlackMoves += 1
			}
		}

		// lock unlocks after next successful move
		if !g.delay_lock {
			g.locked.X = -1
			g.locked.Y = -1
		}
		g.delay_lock = false
		// change player to move
		g.whiteToMove = !g.whiteToMove
		if (g.isReplay || !opponent.IsHuman()) && g.MoveDelay != nil {
			time.Sleep(time.Duration(*g.MoveDelay) * time.Millisecond)
		}
	}
	return nil
}

func (g *Game) Draw(screen *ebiten.Image) {
	// end screen
	if !g.active {
		screen.Fill(color.White)
		score := g.Score()
		winner := "Black"
		if score >= 0.0 {
			winner = "White"
		}
		face := basicfont.Face7x13
		txt := fmt.Sprintf("%s player won! Score: %.2f", winner, score)
		centerX := 0.5*float64(g.Dymension*(g.SquareSize+g.BorderSize)+g.BorderSize) - float64(face.Width*len(txt))/2
		centerY := 0.5 * float64(g.Dymension*(g.SquareSize+g.BorderSize)+g.BorderSize)
		text.Draw(screen, txt, face, int(centerX), int(centerY), color.Black)
		return
	}

	// draw board - squares with left and top borders
	for x := 0; x <= g.Dymension+1; x++ {
		for y := 0; y <= g.Dymension+1; y++ {
			x1 := x * (g.BorderSize + g.SquareSize)
			y1 := y * (g.BorderSize + g.SquareSize)
			// draw left border
			if y <= g.Dymension {
				x2 := x1 + g.BorderSize
				y2 := y1 + g.SquareSize + g.BorderSize
				g.drawSquare(screen, x1, y1, x2, y2, color.RGBA{160, 175, 190, 1})
			}
			// draw top border
			if x <= g.Dymension {
				x2 := x1 + g.SquareSize + g.BorderSize
				y2 := y1 + g.BorderSize
				g.drawSquare(screen, x1, y1, x2, y2, color.RGBA{160, 175, 190, 1})
			}

			// draw empty square when inside the board
			if x <= g.Dymension && y <= g.Dymension {
				x1 += g.BorderSize
				y1 += g.BorderSize
				x2 := x1 + g.SquareSize
				y2 := y1 + g.SquareSize
				g.drawSquare(screen, x1, y1, x2, y2, color.RGBA{180, 90, 30, 1})
			}
		}
	}

	// draw pieces
	for piece_y, row := range g.gameState.Board {
		for piece_x, piece := range row {
			if piece == nil {
				continue
			}
			x := (piece_x+1)*(g.SquareSize+g.BorderSize) - g.SquareSize/2
			y := (piece_y+1)*(g.SquareSize+g.BorderSize) - g.SquareSize/2
			clr := color.White
			if !*piece {
				clr = color.Black
			}
			ebitenutil.DrawCircle(screen, float64(x), float64(y), float64(g.SquareSize/2)*0.8, clr)
		}

	}
}

func (g *Game) Layout(outsideWidth, outsideHeight int) (screenWidth, screenHeight int) {
	return g.Size()
}

// --------------------------- ------------------------------------ --------------------------- \\
