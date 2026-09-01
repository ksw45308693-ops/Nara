package procurement

import (
	"encoding/json"
	"testing"
)

func TestFlexIntRejectsFractionsAndNegativeValues(t *testing.T) {
	for _, raw := range []string{"1.5", "-1", "999999999999999999999999", `"1.5"`, `"-1"`, `"999999999999999999999999"`} {
		var count flexInt
		if json.Unmarshal([]byte(raw), &count) == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
