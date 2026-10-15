package main

import "errors"

// Сентинели — це контракт, а не домашнє завдання: тести перевіряють їх через
// errors.Is, тож не перейменовуйте.
var (
	// ErrNotImplemented — тіло, яке ще треба написати вам.
	ErrNotImplemented = errors.New("not implemented: це ваше завдання")
	// ErrMaxIterations — вичерпано жорсткий ліміт кроків.
	ErrMaxIterations = errors.New("iteration limit reached without a final answer")
	// ErrLoopDetected — (++ Advanced) однакові виклики з однаковими аргументами втретє поспіль.
	ErrLoopDetected = errors.New("loop detected: identical tool calls repeated")
)
