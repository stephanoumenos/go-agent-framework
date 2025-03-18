package heart

func Map[SIn ~[]In, In any, SOut ~[]Out, Out any](s Outputer[SIn], fun func(In) Outputer[Out]) Outputer[SOut] {
	return nil
}

// TODO: Change to error outputer
func ForEach[SIn ~[]In, In any](s Outputer[SIn], fun func(In) Outputer[struct{}]) Outputer[struct{}] {
	return nil
}

func Filter[SIn ~[]In, In any](s Outputer[SIn], fun func(In) Outputer[bool]) Outputer[SIn] {
	return nil
}
