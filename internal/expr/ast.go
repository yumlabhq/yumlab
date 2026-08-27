package expr

// span records where a node starts and ends in the scanned source.
type span struct {
	off int
	end int
}

func (s span) Off() int { return s.off }
func (s span) End() int { return s.end }

// node is a parsed expression fragment. The AST exists only to extract
// references; it is never evaluated.
type node interface {
	Off() int
	End() int
}

type identNode struct {
	span
	name string
}

type literalNode struct {
	span
	str      string // decoded value for strings, raw text otherwise
	isString bool
}

// propNode is target.name. name is "*" for the wildcard dereference.
type propNode struct {
	span
	target node
	name   string
}

// indexNode is target[index].
type indexNode struct {
	span
	target node
	index  node
}

type callNode struct {
	span
	name string
	args []node
}

type unaryNode struct {
	span
	op string
	x  node
}

type binaryNode struct {
	span
	op   string
	x, y node
}

type groupNode struct {
	span
	x node
}
