package hap

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestShortType(t *testing.T) {
	require.Equal(t, "22", ShortType("00000022-0000-1000-8000-0026BB765291"))
	require.Equal(t, "22", ShortType("22"))
	require.Equal(t, "21B", ShortType("0000021b-0000-1000-8000-0026bb765291"))
	require.Equal(t, "E863F120-079E-48FF-8F27-9C2605A29F52", ShortType("E863F120-079E-48FF-8F27-9C2605A29F52"))
	require.Equal(t, "0", ShortType("00000000-0000-1000-8000-0026BB765291"))
}

func TestCharValueFalse(t *testing.T) {
	// "value": false and "ev": false must not be omitted
	var v any = false
	ev := false
	b, err := json.Marshal(charValues{Value: []CharValue{
		{AID: 1, IID: 195, Value: &v},
		{AID: 1, IID: 162, Event: &ev},
	}})
	require.NoError(t, err)
	require.Equal(t, `{"characteristics":[{"aid":1,"iid":195,"value":false},{"aid":1,"iid":162,"ev":false}]}`, string(b))
}

func TestDecodeEvent(t *testing.T) {
	items, err := DecodeEvent(strings.NewReader(`{"characteristics":[{"aid":1,"iid":162,"value":true}]}`))
	require.NoError(t, err)
	require.Len(t, items, 1)
	require.Equal(t, uint64(162), items[0].IID)
	require.Equal(t, true, *items[0].Value)
}
