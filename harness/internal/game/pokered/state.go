// Package pokered turns Pokemon Red's RAM into a typed game state.
//
// Every address comes from pret/pokered's symbol file (pokered.sym), so the
// names below match the disassembly: wCurMap, wPartyMon1HP, and so on.
package pokered

import (
	"context"
	"fmt"
	"strings"

	"harness/internal/emulator"
)

// The decoder reads one contiguous block of work RAM per step instead of a
// dozen tiny requests. Addresses below are absolute; offset() makes them
// block-relative.
const (
	blockStart = 0xCF00
	blockEnd   = 0xD600 // exclusive

	wEnemyMonSpecies = 0xCFE5
	wEnemyMonHP      = 0xCFE6 // 2 bytes, big-endian
	wEnemyMonLevel   = 0xCFF3
	wBattleMonHP     = 0xD015
	wIsInBattle      = 0xD057 // 0 none, 1 wild, 2 trainer, 0xFF lost
	wPartyCount      = 0xD163
	wPartySpecies    = 0xD164 // up to 6 species IDs
	wPartyMon1       = 0xD16B
	partyMonSize     = 0x2C // wPartyMon2 - wPartyMon1
	monHP            = 0x01 // offsets inside a party_struct
	monLevel         = 0x21
	monMaxHP         = 0x22
	wPokedexOwned    = 0xD2F7 // 19-byte bit field
	wPlayerName      = 0xD158 // game charset; first byte is a letter once the player is named
	wPlayerMoney     = 0xD347 // 3 bytes, binary-coded decimal
	wObtainedBadges  = 0xD356 // bit field, bit 0 = Boulder
	wCurMap          = 0xD35E
	wYCoord          = 0xD361
	wXCoord          = 0xD362

	// In the low block (see screen.go).
	wSpritePlayerStateData1FacingDirection = 0xC109
)

var badgeNames = [8]string{"Boulder", "Cascade", "Thunder", "Rainbow", "Soul", "Marsh", "Volcano", "Earth"}

type Mon struct {
	Species string `json:"species"`
	Level   int    `json:"level"`
	HP      int    `json:"hp"`
	MaxHP   int    `json:"max_hp,omitempty"` // unknown for the enemy
}

type State struct {
	InGame   bool     `json:"in_game"` // false on the title screen and before the player is named
	MapID    uint8    `json:"map_id"`
	MapName  string   `json:"map"`
	X        int      `json:"x"`
	Y        int      `json:"y"`
	Facing   string   `json:"facing"`
	Battle   string   `json:"battle"` // "none", "wild", "trainer", "lost"
	Badges   []string `json:"badges"`
	Money    int      `json:"money"`
	DexOwned int      `json:"dex_owned"`
	Party    []Mon    `json:"party"`
	Enemy    *Mon     `json:"enemy,omitempty"` // only in battle
	Screen   Screen   `json:"screen"`
}

// Read fetches the RAM the decoder needs and decodes it.
func Read(ctx context.Context, emu emulator.Emulator) (State, error) {
	block, err := emu.ReadMem(ctx, blockStart, blockEnd-blockStart)
	if err != nil {
		return State{}, fmt.Errorf("read wram: %w", err)
	}
	low, err := emu.ReadMem(ctx, lowStart, lowEnd-lowStart)
	if err != nil {
		return State{}, fmt.Errorf("read sprites and tile map: %w", err)
	}
	s, err := Decode(block, low[wSpritePlayerStateData1FacingDirection-lowStart])
	if err != nil {
		return s, err
	}
	s.Screen, err = readScreen(ctx, emu, low, block)
	return s, err
}

// Decode is pure: bytes in, State out. That makes it testable without an
// emulator (see state_test.go).
func Decode(block []byte, facing byte) (State, error) {
	if len(block) != blockEnd-blockStart {
		return State{}, fmt.Errorf("block is %d bytes, want %d", len(block), blockEnd-blockStart)
	}
	b := func(addr int) byte { return block[addr-blockStart] }
	u16 := func(addr int) int { return int(b(addr))<<8 | int(b(addr+1)) }

	s := State{
		InGame: b(wPlayerName) >= 0x80 && b(wPlayerName) < 0xC0, // A-Z, a-z
		MapID:  b(wCurMap),
		X:      int(b(wXCoord)),
		Y:      int(b(wYCoord)),
		Facing: facingName(facing),
		Battle: battleName(b(wIsInBattle)),
		Money:  bcd(b(wPlayerMoney), b(wPlayerMoney+1), b(wPlayerMoney+2)),
	}
	s.MapName = mapNames[s.MapID]
	if s.MapName == "" {
		s.MapName = fmt.Sprintf("Unknown map 0x%02X", s.MapID)
	}

	badges := b(wObtainedBadges)
	for i, name := range badgeNames {
		if badges&(1<<i) != 0 {
			s.Badges = append(s.Badges, name)
		}
	}

	for i := range 19 {
		s.DexOwned += popcount(b(wPokedexOwned + i))
	}

	// A party count above 6 means RAM isn't initialised yet (title screen).
	if n := int(b(wPartyCount)); n <= 6 {
		for i := range n {
			base := wPartyMon1 + i*partyMonSize
			s.Party = append(s.Party, Mon{
				Species: species(b(wPartySpecies + i)),
				Level:   int(b(base + monLevel)),
				HP:      u16(base + monHP),
				MaxHP:   u16(base + monMaxHP),
			})
		}
	}

	if s.Battle == "wild" || s.Battle == "trainer" {
		s.Enemy = &Mon{
			Species: species(b(wEnemyMonSpecies)),
			Level:   int(b(wEnemyMonLevel)),
			HP:      u16(wEnemyMonHP),
		}
	}
	return s, nil
}

// Summary renders the state as the short text block a model reads each step.
func (s State) Summary() string {
	var sb strings.Builder
	if !s.InGame {
		// Before a game is started, map and position are leftovers (they read
		// as Pallet Town 0,0), so say so instead of letting the model chase them.
		sb.WriteString("The game has not started yet: you are on the boot, title or intro screens. Map and position are not valid until you are in your bedroom.\n")
		sb.WriteString("Press start or a to get through the title screen and menus, and a to advance dialog. Choose NEW GAME, and pick a preset name when asked.")
		if s.Screen.Text != "" {
			fmt.Fprintf(&sb, "\nOn-screen text:\n%s", s.Screen.Text)
		}
		return sb.String()
	}
	fmt.Fprintf(&sb, "Map: %s (x=%d, y=%d, facing %s)\n", s.MapName, s.X, s.Y, s.Facing)
	if len(s.Party) == 0 {
		sb.WriteString("Party: none yet\n")
	} else {
		mons := make([]string, len(s.Party))
		for i, m := range s.Party {
			mons[i] = fmt.Sprintf("%s L%d %d/%d HP", m.Species, m.Level, m.HP, m.MaxHP)
		}
		fmt.Fprintf(&sb, "Party: %s\n", strings.Join(mons, ", "))
	}
	if s.Enemy != nil {
		fmt.Fprintf(&sb, "Battle: %s vs %s L%d (%d HP)\n", s.Battle, s.Enemy.Species, s.Enemy.Level, s.Enemy.HP)
	} else {
		sb.WriteString("Battle: none\n")
	}
	fmt.Fprintf(&sb, "Badges: %d %v | Money: $%d | Pokedex owned: %d", len(s.Badges), s.Badges, s.Money, s.DexOwned)
	if s.Screen.Text != "" {
		fmt.Fprintf(&sb, "\nOn-screen text:\n%s", s.Screen.Text)
	}
	if s.Screen.Map != "" {
		fmt.Fprintf(&sb, "\nMap around you (@ you, N person, . walkable, # blocked; up is north):\n%s", s.Screen.Map)
	}
	return sb.String()
}

func species(id byte) string {
	if n, ok := speciesNames[id]; ok {
		return n
	}
	return fmt.Sprintf("Unknown 0x%02X", id)
}

func facingName(v byte) string {
	switch v {
	case 0x0:
		return "down"
	case 0x4:
		return "up"
	case 0x8:
		return "left"
	case 0xC:
		return "right"
	}
	return fmt.Sprintf("0x%02X", v)
}

func battleName(v byte) string {
	switch v {
	case 0:
		return "none"
	case 1:
		return "wild"
	case 2:
		return "trainer"
	case 0xFF:
		return "lost"
	}
	return fmt.Sprintf("0x%02X", v)
}

// bcd decodes binary-coded decimal: each nibble is one digit, so the bytes
// 0x00 0x30 0x00 mean $3000.
func bcd(bs ...byte) int {
	n := 0
	for _, x := range bs {
		n = n*100 + int(x>>4)*10 + int(x&0x0F)
	}
	return n
}

func popcount(x byte) int {
	n := 0
	for ; x != 0; x &= x - 1 {
		n++
	}
	return n
}
