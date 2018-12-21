package libs

type CounterIft interface {
	Count() int64
	CountN(int64) int64
}
