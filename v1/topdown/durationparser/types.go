package durationparser

//go:generate pigeon -o duration_parser.go duration.peg

// Result holds the parsed components of a duration string.
type Result struct {
	Sign     any   // nil or string ("-" / "+")
	Segments []any // each element is a Segment
}

// Segment holds a single parsed segment (e.g. Digits="1.5", Unit="d").
type Segment struct {
	Digits string
	Unit   string
}
