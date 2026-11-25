package worker

import (
	"crypto/rand"
	"math/big"
)

func randInt(upper *big.Int) (*big.Int, error) {
	return rand.Int(rand.Reader, upper)
}
