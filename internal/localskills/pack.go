package localskills

import (
	"fmt"
	"os"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// Pack is one skill as a tar the Library imports (<name>/SKILL.md and the
// rest): only the files the policy lets through, none of it if the skill is not
// eligible (a credential, a link out, too big) or holds a file name the library
// refuses. The archive is the platform's own bundle format (internal/tarbundle).
func (s *Scanner) Pack(sk Skill) ([]byte, error) {
	if !sk.Eligible {
		return nil, fmt.Errorf("the skill %s cannot be packed: %s", sk.Name, sk.Reason)
	}
	if sk.PublishProblem != "" {
		return nil, fmt.Errorf("the skill %s cannot be added to the library: %s", sk.Name, sk.PublishProblem)
	}
	files, err := s.Files(sk.Dir)
	if err != nil {
		return nil, err
	}
	bundle := make(map[string][]byte, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f.Abs)
		if err != nil {
			return nil, err
		}
		bundle[sk.Name+"/"+f.Rel] = data
	}
	return tarbundle.PackFiles(bundle, tarbundle.ContentLimits)
}
