package collect

import "testing"

func TestParseCPUStat(t *testing.T) {
	const data = `usage_usec 12345
user_usec 6000
system_usec 6345
nr_periods 100
nr_throttled 7
throttled_usec 4200`

	throttled, nr := parseCPUStat(data)
	if throttled != 4200 {
		t.Errorf("throttled_usec = %d, want 4200", throttled)
	}
	if nr != 7 {
		t.Errorf("nr_throttled = %d, want 7", nr)
	}
}

func TestParsePSISome(t *testing.T) {
	const data = `some avg10=0.50 avg60=0.10 avg300=0.02 total=987654
full avg10=0.00 avg60=0.00 avg300=0.00 total=123`

	if got := parsePSISome(data); got != 987654 {
		t.Errorf("psi some total = %d, want 987654", got)
	}
	if got := parsePSISome("garbage\n"); got != 0 {
		t.Errorf("psi some on garbage = %d, want 0", got)
	}
}
