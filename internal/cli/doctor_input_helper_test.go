package cli

import "testing"

func mustDoctorInput(t *testing.T, dir string) *doctorInput {
	t.Helper()
	in, err := loadDoctorInput(dir)
	if err != nil {
		t.Fatal(err)
	}
	return in
}
