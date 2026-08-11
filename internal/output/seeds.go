package output

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"

	"github.com/usestring/gqlcrawl/internal/model"
)

func WriteSeedJSONL(writer io.Writer, seeds []model.Seed) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, seed := range seeds {
		if err := encoder.Encode(seed); err != nil {
			return fmt.Errorf("write seed JSONL: %w", err)
		}
	}
	return nil
}

func WriteSeedLines(writer io.Writer, seeds []model.Seed) error {
	buffered := bufio.NewWriter(writer)
	for _, seed := range seeds {
		if _, err := fmt.Fprintln(buffered, seed.Value); err != nil {
			return fmt.Errorf("write seed lines: %w", err)
		}
	}
	if err := buffered.Flush(); err != nil {
		return fmt.Errorf("write seed lines: %w", err)
	}
	return nil
}
