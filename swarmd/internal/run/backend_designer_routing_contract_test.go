package run

type errContractRenderFailed struct{}

func (errContractRenderFailed) Error() string { return "render failed" }
