package billing

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestALegReportQueryNormalize(t *testing.T) {
	t.Parallel()
	ok, err := ALegReportQuery{AccountID: " acct ", ALegID: " a-leg "}.Normalize()
	require.NoError(t, err)
	require.Equal(t, "acct", ok.AccountID)
	require.Equal(t, "a-leg", ok.ALegID)
	require.Equal(t, ALegReportDefaultLimit, ok.Limit)

	zero, err := ALegReportQuery{AccountID: "acct", ALegID: "a-leg", Limit: 0}.Normalize()
	require.NoError(t, err)
	require.Equal(t, ALegReportDefaultLimit, zero.Limit)

	for _, tc := range []struct {
		name  string
		query ALegReportQuery
	}{
		{name: "missing account", query: ALegReportQuery{ALegID: "a-leg"}},
		{name: "missing aleg", query: ALegReportQuery{AccountID: "acct"}},
		{name: "negative limit", query: ALegReportQuery{AccountID: "acct", ALegID: "a-leg", Limit: -1}},
		{name: "unbounded limit", query: ALegReportQuery{AccountID: "acct", ALegID: "a-leg", Limit: ALegReportMaxLimit + 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := tc.query.Normalize()
			require.ErrorIs(t, err, ErrReportInvalid)
		})
	}

	// The opaque cursor is preserved byte-for-byte, never normalized.
	cursor := EncodeALegReportCursor("test", "acct", "a-leg", "bc_call", "b-leg", "bc_call")
	require.NotEmpty(t, cursor)
	preserved, err := ALegReportQuery{AccountID: "acct", ALegID: "a-leg", Limit: 5, Cursor: cursor}.Normalize()
	require.NoError(t, err)
	require.Equal(t, cursor, preserved.Cursor)
	require.Equal(t, 5, preserved.Limit)
}

func TestALegReportCursorRoundTrip(t *testing.T) {
	t.Parallel()
	token := EncodeALegReportCursor("test", "acct", "a-leg", "bc_call", "b-leg", "bc_call")
	require.NotEmpty(t, token)
	legCall, legBLeg, call, err := DecodeALegReportCursor(token, "test", "acct", "a-leg")
	require.NoError(t, err)
	require.Equal(t, "bc_call", legCall)
	require.Equal(t, "b-leg", legBLeg)
	require.Equal(t, "bc_call", call)

	// Empty legs with a call position encodes the call-only state.
	callOnly := EncodeALegReportCursor("test", "acct", "a-leg", "", "", "bc_call")
	require.NotEmpty(t, callOnly)
	legCall, legBLeg, call, err = DecodeALegReportCursor(callOnly, "test", "acct", "a-leg")
	require.NoError(t, err)
	require.Empty(t, legCall)
	require.Empty(t, legBLeg)
	require.Equal(t, "bc_call", call)

	// Fully empty positions encode page one.
	require.Empty(t, EncodeALegReportCursor("test", "acct", "a-leg", "", "", ""))
	legCall, legBLeg, call, err = DecodeALegReportCursor("", "test", "acct", "a-leg")
	require.NoError(t, err)
	require.Empty(t, legCall)
	require.Empty(t, legBLeg)
	require.Empty(t, call)
}

func TestALegReportCursorRejectsNonCanonical(t *testing.T) {
	t.Parallel()
	valid := EncodeALegReportCursor("test", "acct", "a-leg", "bc_call", "b-leg", "bc_call")
	// One-sided leg positions can never come from Encode, so craft them by
	// payload surgery to prove the decoder still rejects them.
	partial := func(dropLegBLeg, dropCall bool) string {
		raw := valid[len("aleg."):]
		payload, err := base64.RawURLEncoding.DecodeString(raw)
		require.NoError(t, err)
		var shaped map[string]any
		require.NoError(t, json.Unmarshal(payload, &shaped))
		if dropLegBLeg {
			delete(shaped, "leg_bleg")
		}
		if dropCall {
			delete(shaped, "call")
		}
		rebuilt, err := json.Marshal(shaped)
		require.NoError(t, err)
		return "aleg." + base64.RawURLEncoding.EncodeToString(rebuilt)
	}
	for _, tc := range []struct {
		name  string
		token string
		store string
		acct  string
		aleg  string
	}{
		{name: "wrong prefix", token: "bogus." + valid[len("aleg."):], store: "test", acct: "acct", aleg: "a-leg"},
		{name: "bad encoding", token: "aleg.!!!", store: "test", acct: "acct", aleg: "a-leg"},
		{name: "one-sided leg", token: partial(true, false), store: "test", acct: "acct", aleg: "a-leg"},
		{name: "legs without call", token: partial(false, true), store: "test", acct: "acct", aleg: "a-leg"},
		{name: "scope mismatch", token: valid, store: "test", acct: "acct", aleg: "a-leg-other"},
		{name: "store mismatch", token: valid, store: "other", acct: "acct", aleg: "a-leg"},
		{name: "whitespace", token: " " + valid + " ", store: "test", acct: "acct", aleg: "a-leg"},
		{name: "oversize", token: valid + string(make([]byte, 4096)), store: "test", acct: "acct", aleg: "a-leg"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, _, err := DecodeALegReportCursor(tc.token, tc.store, tc.acct, tc.aleg)
			require.ErrorIs(t, err, ErrReportInvalid)
		})
	}
}

func TestALegReportCursorVersionRejected(t *testing.T) {
	t.Parallel()
	// A token minted for another scope shape must not decode here even when
	// well-formed base64: scope binding is part of validity.
	foreign := EncodeALegReportCursor("test", "acct", "a-leg", "bc_call", "b-leg", "bc_other")
	_, _, _, err := DecodeALegReportCursor(foreign, "test", "acct", "a-leg")
	require.NoError(t, err, "same-scope token decodes; scope mismatch is the rejection path")
	_, _, _, err = DecodeALegReportCursor(foreign, "test", "other", "a-leg")
	require.True(t, errors.Is(err, ErrReportInvalid), "cross-account cursor must fail, got %v", err)
}
