package output

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/usestring/gqlcrawl/internal/model"
)

func WriteJSONL(writer io.Writer, results []model.Result) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	for _, result := range results {
		if err := encoder.Encode(result); err != nil {
			return fmt.Errorf("write JSONL: %w", err)
		}
	}
	return nil
}
