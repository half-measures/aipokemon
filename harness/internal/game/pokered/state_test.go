package pokered

import (
	"slices"
	"strings"
	"testing"
)

// newBlock returns a zeroed RAM block plus a setter that takes absolute
// addresses, so tests read like the symbol file.
func newBlock() ([]byte, func(addr int, v ...byte)) {
	block := make([]byte, blockEnd-blockStart)
	return block, func(addr int, v ...byte) { copy(block[addr-blockStart:], v) }
}

func TestDecodeStartingTown(t *testing.T) {
	block, set := newBlock()
	set(wCurMap, 0x00) // Pallet Town
	set(wXCoord, 5)
	set(wYCoord, 6)
	set(wPlayerMoney, 0x00, 0x30, 0x00)
	set(wObtainedBadges, 0b0000_0011) // Boulder + Cascade
	set(wPokedexOwned, 0b0000_0101)   // two owned
	set(wPartyCount, 1)
	set(wPartySpecies, 0xB0) // Charmander (internal ID)
	set(wPartyMon1+monHP, 0x00, 0x14)
	set(wPartyMon1+monLevel, 6)
	set(wPartyMon1+monMaxHP, 0x00, 0x16)

	s, err := Decode(block, 0x4)
	if err != nil {
		t.Fatal(err)
	}
	if s.MapName != "Pallet Town" || s.X != 5 || s.Y != 6 || s.Facing != "up" {
		t.Errorf("position = %q (%d,%d) %s", s.MapName, s.X, s.Y, s.Facing)
	}
	if s.Money != 3000 {
		t.Errorf("money = %d, want 3000", s.Money)
	}
	if !slices.Equal(s.Badges, []string{"Boulder", "Cascade"}) {
		t.Errorf("badges = %v", s.Badges)
	}
	if s.DexOwned != 2 {
		t.Errorf("dex owned = %d, want 2", s.DexOwned)
	}
	want := Mon{Species: "Charmander", Level: 6, HP: 20, MaxHP: 22}
	if len(s.Party) != 1 || s.Party[0] != want {
		t.Errorf("party = %+v, want [%+v]", s.Party, want)
	}
	if s.Battle != "none" || s.Enemy != nil {
		t.Errorf("battle = %s, enemy = %+v", s.Battle, s.Enemy)
	}
}

func TestDecodeWildBattle(t *testing.T) {
	block, set := newBlock()
	set(wIsInBattle, 1)
	set(wEnemyMonSpecies, 0x24) // Pidgey
	set(wEnemyMonLevel, 3)
	set(wEnemyMonHP, 0x00, 0x0F)

	s, err := Decode(block, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := Mon{Species: "Pidgey", Level: 3, HP: 15}
	if s.Battle != "wild" || s.Enemy == nil || *s.Enemy != want {
		t.Errorf("battle = %s, enemy = %+v, want %+v", s.Battle, s.Enemy, want)
	}
}

func TestDecodeUninitialisedParty(t *testing.T) {
	block, set := newBlock()
	set(wPartyCount, 0xFF) // title screen garbage
	s, err := Decode(block, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Party) != 0 {
		t.Errorf("party = %+v, want empty", s.Party)
	}
}

func TestBCD(t *testing.T) {
	for _, tc := range []struct {
		in   []byte
		want int
	}{
		{[]byte{0x00, 0x00, 0x00}, 0},
		{[]byte{0x00, 0x30, 0x00}, 3000},
		{[]byte{0x99, 0x99, 0x99}, 999999},
	} {
		if got := bcd(tc.in...); got != tc.want {
			t.Errorf("bcd(% X) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestScreenText(t *testing.T) {
	tiles := make([]byte, screenW*screenH)
	// Row 14: "│Hi RED!│" inside a box, the rest map graphics (0x00).
	copy(tiles[14*screenW:], []byte{0x7C, 0x87, 0xA8, 0x7F, 0x91, 0x84, 0x83, 0xE7, 0x7C})
	if got, want := screenText(tiles), "│Hi RED!│"; got != want {
		t.Errorf("screenText = %q, want %q", got, want)
	}
}

func TestWalkGrid(t *testing.T) {
	tiles := make([]byte, screenW*screenH) // all tile 0x00
	sprites := make([]byte, 16*16)
	// Player sprite at the fixed on-screen position (x=64, y=60): block (4,4).
	copy(sprites[0:], []byte{1, 0, 0, 0, 60, 0, 64})
	// An NPC one block to the right.
	copy(sprites[16:], []byte{5, 0, 0, 0, 60, 0, 80})
	grid := walkGrid(tiles, sprites, map[byte]bool{0x00: true})
	rows := strings.Split(grid, "\n")
	if len(rows) != 9 || rows[4] != "....@N...." {
		t.Errorf("grid =\n%s", grid)
	}
	if blocked := walkGrid(tiles, sprites, map[byte]bool{}); !strings.HasPrefix(blocked, "##########") {
		t.Errorf("unwalkable grid =\n%s", blocked)
	}
}
