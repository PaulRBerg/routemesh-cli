// Package evm validates the bounded EVM values used by evidence commands.
package evm

import (
	"fmt"
	"math/big"
	"regexp"
	"strconv"
	"strings"
)

var (
	decimalChainID = regexp.MustCompile(`^[1-9][0-9]*$`)
	hexQuantity    = regexp.MustCompile(`^0x(?:0|[1-9a-fA-F][0-9a-fA-F]*)$`)
	hexData        = regexp.MustCompile(`^0x(?:[0-9a-fA-F]{2})*$`)
)

func ParseChainID(raw string) (uint64, error) {
	if !decimalChainID.MatchString(raw) {
		return 0, fmt.Errorf("chain ID must be a canonical positive decimal uint64")
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || value == 0 {
		return 0, fmt.Errorf("chain ID must be a canonical positive decimal uint64")
	}
	return value, nil
}

func ParseQuantity(raw string) (uint64, error) {
	if !hexQuantity.MatchString(raw) {
		return 0, fmt.Errorf("value must be a canonical 0x-prefixed quantity")
	}
	value, err := strconv.ParseUint(raw[2:], 16, 64)
	if err != nil {
		return 0, fmt.Errorf("quantity exceeds uint64")
	}
	return value, nil
}

func ParseBigQuantity(raw string) (*big.Int, error) {
	if !hexQuantity.MatchString(raw) {
		return nil, fmt.Errorf("value must be a canonical 0x-prefixed quantity")
	}
	value, ok := new(big.Int).SetString(raw[2:], 16)
	if !ok {
		return nil, fmt.Errorf("invalid quantity")
	}
	return value, nil
}

func Quantity(value uint64) string {
	return fmt.Sprintf("0x%x", value)
}

func ValidateHash(raw string) error {
	return validateFixedHex(raw, 32, "hash")
}

func ValidateAddress(raw string) error {
	return validateFixedHex(raw, 20, "address")
}

func ValidateTopic(raw string) error {
	return validateFixedHex(raw, 32, "topic")
}

func ValidateData(raw string) error {
	if !hexData.MatchString(raw) {
		return fmt.Errorf("data must be 0x-prefixed, even-width hexadecimal")
	}
	return nil
}

func validateFixedHex(raw string, bytes int, label string) error {
	if len(raw) != 2+(bytes*2) || !strings.HasPrefix(raw, "0x") {
		return fmt.Errorf("%s must be a full %d-byte 0x-prefixed value", label, bytes)
	}
	for _, char := range raw[2:] {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')) {
			return fmt.Errorf("%s must be hexadecimal", label)
		}
	}
	return nil
}
