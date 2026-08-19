package baichuan

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestSnapRequestBody(t *testing.T) {
	t.Parallel()

	data, err := xml.Marshal(snapRequestBody(0, "main"))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	got := string(data)
	for _, want := range []string{
		`<Snap version="1.1">`,
		`<channelId>0</channelId>`,
		`<logicChannel>0</logicChannel>`,
		`<time>0</time>`,
		`<fullFrame>0</fullFrame>`,
		`<streamType>main</streamType>`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("snap request %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "fileName") || strings.Contains(got, "pictureSize") {
		t.Fatalf("snap request must not carry reply-only fields: %q", got)
	}
}
