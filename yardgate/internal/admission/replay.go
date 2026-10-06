package admission

import "fmt"

// ReplayEvents faithfully re-executes events on the state in sequence order:
// job events are re-planned and committed, correction events re-apply their
// weight change. Because planning, discretization and grid application are
// deterministic, the resulting grid equals the incrementally maintained one
// bit for bit (verified in tests).
func ReplayEvents(st *State, events []Event) error {
	for _, ev := range events {
		switch ev.Type {
		case EventCorrection:
			if ev.Correction == nil {
				return fmt.Errorf("event %d: correction event without payload", ev.Seq)
			}
			loads, _, _ := st.CorrectionLoads(ev.Correction.TargetSeq, ev.Correction.NewWeightKN)
			pd := st.GridDeltas(loads)
			st.CommitCorrection(ev.Correction.TargetSeq, ev.Correction.NewWeightKN)
			st.Grid.Apply(pd)
		case EventPlace, EventRemove, EventReshuffle:
			if ev.Job == nil {
				return fmt.Errorf("event %d: job event without payload", ev.Seq)
			}
			loads, err := st.Plan(*ev.Job)
			if err != nil {
				return fmt.Errorf("event %d: replay plan failed: %w", ev.Seq, err)
			}
			pd := st.GridDeltas(loads)
			st.CommitJob(*ev.Job, ev.Seq)
			st.Grid.Apply(pd)
		default:
			return fmt.Errorf("event %d: unknown type %q", ev.Seq, ev.Type)
		}
	}
	return nil
}

// ReplayCounterfactual re-executes job events with weight overrides applied
// (correction events are no-ops here because the overrides already carry
// their effect). Every job event is admission-checked against the evolving
// counterfactual state; the sequence numbers of jobs that would have been
// rejected are returned. Jobs are applied regardless, following the actual
// historical trajectory.
func ReplayCounterfactual(st *State, events []Event, overrides map[int64]float64, topK int) ([]int64, error) {
	var rejected []int64
	for _, ev := range events {
		if ev.Type == EventCorrection {
			continue
		}
		j := *ev.Job
		if w, ok := overrides[ev.Seq]; ok && j.Type == Place {
			j.WeightKN = w
		}
		loads, err := st.Plan(j)
		if err != nil {
			return nil, fmt.Errorf("event %d: counterfactual plan failed: %w", ev.Seq, err)
		}
		pd := st.GridDeltas(loads)
		if viol := st.Grid.Evaluate(pd, topK); len(viol) > 0 {
			rejected = append(rejected, ev.Seq)
		}
		st.CommitJob(j, ev.Seq)
		st.Grid.Apply(pd)
	}
	return rejected, nil
}
