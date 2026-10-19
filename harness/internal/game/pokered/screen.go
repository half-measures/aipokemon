package pokered

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"harness/internal/emulator"
)

// The screen is also in RAM: wTileMap is the 20x18 grid of tile IDs the game
// copies to the display. Decoding it gives a text-only model what a vision
// model would get from the screenshot: dialog, menus, and walls.
const (
	wSpriteStateData1    = 0xC100 // 16 sprites x 16 bytes; sprite 0 is the player
	wTileMap             = 0xC3A0
	screenW, screenH     = 20, 18
	lowStart             = wSpriteStateData1
	lowEnd               = wTileMap + screenW*screenH // exclusive
	wTilesetCollisionPtr = 0xD530                     // 2 bytes, little-endian
	wGrassTile           = 0xD535
	hTileAnimations      = 0xFFD7 // non-zero on outdoor tilesets, where grass is walkable
)

// Screen is what's visible right now, rendered as text.
type Screen struct {
	Text string `json:"text,omitempty"` // dialog and menu text, empty when none is open
	Map  string `json:"map"`            // 9x10 grid of 16x16 walk squares around the player
	// DialogOnly is true when the bottom text box is open and no menu cursor
	// (▶) is anywhere on screen: the only sensible input is a, so the harness
	// can press it without asking the model.
	DialogOnly bool `json:"dialog_only"`
}

// charmap covers the font tiles (0x79 and up) from pret/pokered's
// constants/charmap.asm. Everything below 0x79 is map graphics.
var charmap = func() [256]string {
	var m [256]string
	set := func(start byte, chars string) {
		i := int(start)
		for _, r := range chars {
			m[i] = string(r)
			i++
		}
	}
	set(0x79, "┌─┐│└┘ ")
	set(0x80, "ABCDEFGHIJKLMNOPQRSTUVWXYZ():;[]")
	set(0xA0, "abcdefghijklmnopqrstuvwxyzé")
	m[0xBB], m[0xBC], m[0xBD], m[0xBE], m[0xBF] = "'d", "'l", "'s", "'t", "'v"
	m[0xE0], m[0xE1], m[0xE2], m[0xE3], m[0xE4], m[0xE5] = "'", "PK", "MN", "-", "'r", "'m"
	set(0xE6, "?!.")
	set(0xEC, "▷▶▼♂¥×./,♀0123456789")
	return m
}()

// readScreen needs the low block (sprites + tile map) and the main block,
// which holds the pointer to this tileset's list of walkable tiles.
func readScreen(ctx context.Context, emu emulator.Emulator, low, hiBlock []byte) (Screen, error) {
	anim, err := emu.ReadMem(ctx, hTileAnimations, 1)
	if err != nil {
		return Screen{}, err
	}
	hi := func(addr int) byte { return hiBlock[addr-blockStart] }

	walkable := map[byte]bool{}
	if anim[0] > 0 && hi(wGrassTile) != 0xFF {
		walkable[hi(wGrassTile)] = true
	}
	ptr := uint16(hi(wTilesetCollisionPtr)) | uint16(hi(wTilesetCollisionPtr+1))<<8
	list, err := emu.ReadMem(ctx, ptr, 0x40)
	if err != nil {
		return Screen{}, fmt.Errorf("read collision list: %w", err)
	}
	for _, t := range list {
		if t == 0xFF {
			break
		}
		walkable[t] = true
	}
	return decodeScreen(low, walkable), nil
}

// decodeScreen is pure, like Decode. low covers wSpriteStateData1..wTileMap end.
func decodeScreen(low []byte, walkable map[byte]bool) Screen {
	tiles := low[wTileMap-lowStart:]
	return Screen{
		Text:       screenText(tiles),
		Map:        walkGrid(tiles, low[:wTileMap-lowStart], walkable),
		DialogOnly: dialogOnly(tiles),
	}
}

const (
	tileBoxTopLeft    = 0x79 // ┌
	tileBoxBottomLeft = 0x7D // └
	tileCursor        = 0xED // ▶
)

// dialogOnly: the standard text box spans rows 12-17, so its corners sit at
// (row 12, col 0) and (row 17, col 0).
func dialogOnly(tiles []byte) bool {
	if tiles[12*screenW] != tileBoxTopLeft || tiles[17*screenW] != tileBoxBottomLeft {
		return false
	}
	return !slices.Contains(tiles, tileCursor)
}

// screenText decodes every row that contains font characters. Map graphics
// use tile IDs below 0x79, so rows without a text box come out blank and
// are dropped.
func screenText(tiles []byte) string {
	var lines []string
	for row := range screenH {
		var sb strings.Builder
		letters := false
		for col := range screenW {
			t := tiles[row*screenW+col]
			c := charmap[t]
			if c == "" {
				c = " "
			}
			if t >= 0x80 && t != 0x7F {
				letters = true
			}
			sb.WriteString(c)
		}
		if letters {
			lines = append(lines, strings.TrimRight(sb.String(), " "))
		}
	}
	return strings.Join(lines, "\n")
}

// walkGrid samples the bottom-left tile of each 2x2 block, the tile the game
// itself checks for collisions, and marks the player and other sprites.
//
//	@ you   N person/object   . walkable   # blocked
func walkGrid(tiles, sprites []byte, walkable map[byte]bool) string {
	const rows, cols = screenH / 2, screenW / 2
	var grid [rows][cols]byte
	for r := range rows {
		for c := range cols {
			if walkable[tiles[(r*2+1)*screenW+c*2]] {
				grid[r][c] = '.'
			} else {
				grid[r][c] = '#'
			}
		}
	}
	for i := range 16 {
		s := sprites[i*16:]
		picture, image, y, x := s[0], s[2], int(s[4]), int(s[6])
		if picture == 0 || image == 0xFF {
			continue // empty slot or off screen
		}
		r, c := (y+4)/16, x/16
		if r < 0 || r >= rows || c < 0 || c >= cols {
			continue
		}
		if i == 0 {
			grid[r][c] = '@'
		} else {
			grid[r][c] = 'N'
		}
	}
	var sb strings.Builder
	for r := range rows {
		sb.Write(grid[r][:])
		if r < rows-1 {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}
