package dependencysecurity_test

import (
	"testing"
	"time"

	jwt "github.com/form3tech-oss/jwt-go"
)

var syntheticKey = []byte("isolated-regression-key")

func parseClaims(t *testing.T, claims jwt.MapClaims) (*jwt.Token, error) {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(syntheticKey)
	if err != nil {
		t.Fatal(err)
	}
	return jwt.Parse(signed, func(token *jwt.Token) (interface{}, error) { return syntheticKey, nil })
}

func TestRegressionJWTRejectsMalformedNumericDates(t *testing.T) {
	for _, field := range []string{"exp", "iat", "nbf"} {
		t.Run(field, func(t *testing.T) {
			token, err := parseClaims(t, jwt.MapClaims{field: "not-a-number"})
			if err == nil || (token != nil && token.Valid) {
				t.Fatalf("accepted malformed %s NumericDate", field)
			}
		})
	}
}

func TestJWTOrdinaryClaimsAndAudience(t *testing.T) {
	token, err := parseClaims(t, jwt.MapClaims{"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Add(-time.Minute).Unix(), "nbf": time.Now().Add(-time.Minute).Unix()})
	if err != nil || token == nil || !token.Valid {
		t.Fatalf("ordinary signed claims rejected: %v", err)
	}
	for _, tc := range []struct {
		name string
		aud  interface{}
		want bool
	}{
		{"string", "expected", true},
		{"string-array", []string{"expected"}, true},
		{"json-array", []interface{}{"expected"}, true},
		{"wrong-array", []string{"other"}, false},
		{"numeric", 42, false},
		{"mixed-array", []interface{}{42}, false},
		{"empty-required", []string{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := (jwt.MapClaims{"aud": tc.aud}).VerifyAudience("expected", true); got != tc.want {
				t.Fatalf("audience result %v want %v", got, tc.want)
			}
		})
	}
}
