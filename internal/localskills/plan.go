package localskills

import (
	"fmt"
	"slices"
	"sort"
)

// Mount is a read-only bind of a host folder at a guest path.
type Mount struct {
	Host  string
	Guest string
}

// Plan is what one sandboxed turn gets: the mounts, and a note for each thing
// that was left out and why (the member reads them).
type Plan struct {
	Mounts []Mount
	Notes  []string
}

// PlanInput is everything BuildPlan reads.
type PlanInput struct {
	Settings   Settings
	Policy     Policy
	Home       string
	MirrorRoot string
	// Borrowed is true for a run another member borrows: only skills the owner
	// ticked for borrowers are mounted.
	Borrowed bool
}

// BuildPlan brings the ticked skills into the mirror and says where to mount
// them. Off, or nothing ticked, is an empty plan. A skill that cannot be used
// is left out with a note, never an error: the turn runs without it.
func BuildPlan(in PlanInput) (Plan, error) {
	var plan Plan
	if !in.Settings.Enabled {
		return plan, nil
	}
	sc, err := ScannerFor(in.Policy, in.Settings)
	if err != nil {
		return plan, err
	}
	found := map[string]Skill{}
	for _, src := range in.Settings.Sources {
		skills, _, err := sc.ScanSource(ExpandHome(src, in.Home))
		if err != nil {
			plan.Notes = append(plan.Notes, fmt.Sprintf("the folder %s cannot be read: %v", src, err))
			continue
		}
		for _, sk := range skills {
			if _, taken := found[sk.Name]; !taken {
				found[sk.Name] = sk
			}
		}
	}
	var wanted []string
	for name, c := range in.Settings.Skills {
		if c.VM {
			wanted = append(wanted, name)
		}
	}
	sort.Strings(wanted)
	mirror := Mirror{Root: in.MirrorRoot}
	// The copies of skills that are no longer ticked go, whoever asks: a
	// borrowed turn mounts fewer skills than an own one, but keeps the others'.
	if err := mirror.Prune(wanted); err != nil {
		return plan, fmt.Errorf("local skills mirror: %w", err)
	}
	agentDirs := union(in.Policy.AgentDirs, in.Settings.AgentDirs)
	var mirrored int64
	count := 0
	for _, name := range wanted {
		if in.Borrowed && !in.Settings.Skills[name].Borrowers {
			continue
		}
		sk, ok := found[name]
		switch {
		case !ok:
			plan.Notes = append(plan.Notes, fmt.Sprintf("the skill %s is ticked but is in none of the folders", name))
			continue
		case !sk.Eligible:
			plan.Notes = append(plan.Notes, fmt.Sprintf("the skill %s is left out: %s", name, sk.Reason))
			continue
		case count >= in.Policy.Limits.Skills:
			plan.Notes = append(plan.Notes, fmt.Sprintf("the skill %s is left out: at most %d skills at once", name, in.Policy.Limits.Skills))
			continue
		case mirrored+sk.Bytes > in.Policy.Limits.MirrorBytes:
			plan.Notes = append(plan.Notes, fmt.Sprintf("the skill %s is left out: the copies would pass %d MiB", name, in.Policy.Limits.MirrorBytes>>20))
			continue
		}
		files, err := sc.Files(sk.Dir)
		if err == nil {
			err = mirror.Sync(name, files)
		}
		if err != nil {
			plan.Notes = append(plan.Notes, fmt.Sprintf("the skill %s is left out: %v", name, err))
			continue
		}
		count++
		mirrored += sk.Bytes
		for _, guest := range ReachPaths(sk, agentDirs, in.Home) {
			m := Mount{Host: mirror.Dir(name), Guest: guest}
			if !slices.Contains(plan.Mounts, m) {
				plan.Mounts = append(plan.Mounts, m)
			}
		}
	}
	return plan, nil
}
