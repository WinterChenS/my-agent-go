package acceptance

import (
	"fmt"
	"time"
)

// waitFocused polls the live accessibility state until the toolbar trigger
// owns focus again. Intermediate misses do not poison the case status.
func (s *shellNative) waitFocused(ref, label string) bool {
	for attempt := 1; attempt <= 10; attempt++ {
		statusBefore := s.out.Status
		limitationsBefore := len(s.out.Limitations)
		ok := s.run(StepSpec{
			Name:                 fmt.Sprintf("focus-return-%s-%02d", label, attempt),
			Command:              "agent-desktop",
			Args:                 []string{"is", ref, "--property", "focused"},
			Timeout:              30 * time.Second,
			ExpectOutputContains: `"result":true`,
		})
		if ok {
			return true
		}
		if s.out.Status == StatusBlocked {
			return false
		}
		s.out.Status = statusBefore
		if len(s.out.Limitations) > limitationsBefore {
			s.out.Limitations = s.out.Limitations[:limitationsBefore]
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.out.Status = StatusFail
	s.out.Limitations = append(s.out.Limitations, "Escape closed the palette but focus did not return to the toolbar trigger")
	return false
}
