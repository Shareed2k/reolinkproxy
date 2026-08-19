package baichuan

import (
	"strings"
	"testing"
)

func TestISPFieldPatching(t *testing.T) {
	t.Parallel()

	blob := `<?xml version="1.0" encoding="UTF-8"?><body><VideoInput version="1.1"><channelId>0</channelId><bright>128</bright><contrast>128</contrast><saturation>128</saturation><hue>128</hue><sharpen>128</sharpen></VideoInput><InputAdvanceCfg version="1.1"><Exposure><mode>auto</mode></Exposure></InputAdvanceCfg></body>`

	patched := ispFieldRes["bright"].ReplaceAllString(blob, "${1}42${2}")
	patched = ispFieldRes["sharpen"].ReplaceAllString(patched, "${1}300${2}")

	if !strings.Contains(patched, "<bright>42</bright>") {
		t.Fatalf("bright not patched:\n%s", patched)
	}
	if !strings.Contains(patched, "<contrast>128</contrast>") {
		t.Fatalf("untouched field must survive byte-exact:\n%s", patched)
	}
	if !strings.Contains(patched, "<mode>auto</mode>") {
		t.Fatalf("unknown blob content must survive:\n%s", patched)
	}
}
