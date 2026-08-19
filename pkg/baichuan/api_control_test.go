package baichuan

import (
	"encoding/xml"
	"strings"
	"testing"
)

// The camera only understands the preset commands "toPos" and "setPos"
// (case-sensitive). Sending anything else makes cmd 19 fail with status 400.
func TestPTZPresetBodyUsesToPosCommand(t *testing.T) {
	t.Parallel()

	data, err := xml.Marshal(ptzPresetBody(0, 1))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got := string(data)
	if !strings.Contains(got, "<command>toPos</command>") {
		t.Fatalf("payload %q does not contain <command>toPos</command>", got)
	}
	if !strings.Contains(got, "<id>1</id>") {
		t.Fatalf("payload %q does not contain preset id", got)
	}
}
