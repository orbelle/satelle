package substratereview

// Batches makes one judge dispatch per area, in Areas order, so each judge's
// manifest stays focused. An area is never split: satelle sizes no dispatch to a
// budget, because the judge's CLI manages its own context (sty_58a7bdfe). The
// constitution rules ride the principles batch. An area with nothing to judge
// yields no batch.
func Batches(in JudgeInput) []JudgeInput {
	byArea := map[string][]JudgeItem{}
	for _, it := range in.Items {
		a := AreaOf(it.Kind)
		byArea[a] = append(byArea[a], it)
	}
	var out []JudgeInput
	for _, area := range Areas {
		b := JudgeInput{Area: area, Scratch: in.Scratch, Items: byArea[area],
			ConstitutionPath: in.ConstitutionPath, OrchestratorModel: in.OrchestratorModel}
		if area == AreaPrinciples {
			b.Rules = in.Rules
		}
		if len(b.Items) == 0 && len(b.Rules) == 0 {
			continue
		}
		out = append(out, b)
	}
	return out
}
