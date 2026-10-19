// Command peek prints the decoded Pokemon Red state once. Handy for checking
// the decoder against what you see on screen.
//
//	go run ./cmd/peek            # text summary
//	go run ./cmd/peek -json      # full state as JSON
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"harness/internal/emulator"
	"harness/internal/game/pokered"
)

func main() {
	emuURL := flag.String("emu", "http://127.0.0.1:8765", "emulator server URL")
	asJSON := flag.Bool("json", false, "print JSON instead of the text summary")
	flag.Parse()

	s, err := pokered.Read(context.Background(), emulator.NewHTTPClient(*emuURL))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(s)
		return
	}
	fmt.Println(s.Summary())
}
