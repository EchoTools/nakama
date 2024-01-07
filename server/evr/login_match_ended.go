package evr

import (
	"fmt"
)

type MatchEnded struct {
	// TODO
}

func (m MatchEnded) Token() string {
	return "SNSMatchEndedv5"
}

func (m *MatchEnded) Symbol() Symbol {
	return SymbolOf(m)
}

func (m *MatchEnded) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return nil },
	})
}

func (m MatchEnded) String() string {
	return fmt.Sprintf("%s()", m.Token())
}
