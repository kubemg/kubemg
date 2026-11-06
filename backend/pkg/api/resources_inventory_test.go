package api

import "testing"

func TestIsDefaultStorageClass(t *testing.T) {
	cases := []struct {
		name        string
		annotations map[string]string
		want        bool
	}{
		{
			name:        "GA annotation true",
			annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "true"},
			want:        true,
		},
		{
			name:        "beta annotation true",
			annotations: map[string]string{"storageclass.beta.kubernetes.io/is-default-class": "true"},
			want:        true,
		},
		{
			name: "both annotations true",
			annotations: map[string]string{
				"storageclass.kubernetes.io/is-default-class":      "true",
				"storageclass.beta.kubernetes.io/is-default-class": "true",
			},
			want: true,
		},
		{
			name:        "GA annotation false",
			annotations: map[string]string{"storageclass.kubernetes.io/is-default-class": "false"},
			want:        false,
		},
		{
			name:        "beta annotation false",
			annotations: map[string]string{"storageclass.beta.kubernetes.io/is-default-class": "false"},
			want:        false,
		},
		{
			name:        "unrelated annotation only",
			annotations: map[string]string{"example.com/something": "true"},
			want:        false,
		},
		{
			name:        "no annotations",
			annotations: nil,
			want:        false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isDefaultStorageClass(tc.annotations)
			if got != tc.want {
				t.Errorf("isDefaultStorageClass(%v) = %v; want %v", tc.annotations, got, tc.want)
			}
		})
	}
}
