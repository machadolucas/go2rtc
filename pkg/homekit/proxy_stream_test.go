package homekit

import (
	"encoding/json"
	"testing"

	"github.com/AlexxIT/go2rtc/pkg/hap/camera"
	"github.com/stretchr/testify/require"
)

const testAccessories = `{"accessories":[{"aid":1,"services":[
{"iid":48,"type":"00000110-0000-1000-8000-0026BB765291","characteristics":[
 {"iid":50,"type":"B0","format":"uint8","perms":["pr","pw","ev"],"value":1,"minValue":0,"maxValue":1},
 {"iid":51,"type":"120","format":"tlv8","perms":["pr","ev"],"value":"AQEB"},
 {"iid":54,"type":"116","format":"tlv8","perms":["pr"],"value":"AgEAAAACAQEAAAIBAg=="},
 {"iid":55,"type":"118","format":"tlv8","perms":["pr","pw"],"value":""},
 {"iid":56,"type":"117","format":"tlv8","perms":["pr","pw"],"value":""}]},
{"iid":192,"type":"43","characteristics":[
 {"iid":196,"type":"8","format":"int","perms":["pr","pw","ev"],"value":50,"minValue":0,"maxValue":100,"unit":"percentage"},
 {"iid":61465,"type":"E863F134-079E-48FF-8F27-9C2605A29F52","format":"uint32","perms":["pr","ev"],"value":2690848248}]}]}]}`

func TestProxyPatchAccessories(t *testing.T) {
	p := &Proxy{}
	body := p.patchAccessories([]byte(testAccessories))

	require.Equal(t, camera.TypeStreamingStatus, p.streamType(51))
	require.Equal(t, camera.TypeSetupEndpoints, p.streamType(55))
	require.Equal(t, camera.TypeSelectedStreamConfiguration, p.streamType(56))
	require.Equal(t, "", p.streamType(50))
	// the list of crypto suites is not changed
	require.Contains(t, string(body), "AgEAAAACAQEAAAIBAg==")

	s := string(body)
	// "in use" (AQEB) became "available" (AQEA)
	require.Contains(t, s, `"value":"AQEA"`)
	require.NotContains(t, s, `"AQEB"`)
	// unknown fields and big integers are preserved
	require.Contains(t, s, `"maxValue":100`)
	require.Contains(t, s, `"unit":"percentage"`)
	require.Contains(t, s, `"value":2690848248`)
	require.True(t, json.Valid(body))
}

func TestProxyPatchCharacteristics(t *testing.T) {
	p := &Proxy{}
	p.patchAccessories([]byte(testAccessories))

	// event only about the streaming status - dropped
	require.Nil(t, p.patchCharacteristics([]byte(`{"characteristics":[{"aid":1,"iid":51,"value":"AQEB"}]}`)))

	// other events - as is
	src := []byte(`{"characteristics":[{"aid":1,"iid":196,"value":80}]}`)
	require.Equal(t, src, p.patchCharacteristics(src))

	// mixed - patched
	b := p.patchCharacteristics([]byte(`{"characteristics":[{"aid":1,"iid":51,"value":"AQEB"},{"aid":1,"iid":196,"value":80}]}`))
	require.Contains(t, string(b), `"AQEA"`)
	require.Contains(t, string(b), `"value":80`)
}
