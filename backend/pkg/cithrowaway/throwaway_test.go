package cithrowaway

import "testing"

func TestThrowawayFails(t *testing.T) {
	t.Fatal("deliberate failure: shows backend-test going red in CI")
}
