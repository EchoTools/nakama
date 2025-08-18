package evr

var TemplateSymbol Symbol = ToSymbol("Template")

type Template struct {
}

func (m *Template) Symbol() Symbol {
	return TemplateSymbol
}

func (m *Template) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{})
}
