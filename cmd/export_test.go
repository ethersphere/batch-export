package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
)

func TestSolelyCanceled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "pure context.Canceled",
			err:  context.Canceled,
			want: true,
		},
		{
			name: "wrapped context.Canceled",
			err:  fmt.Errorf("wrap: %w", context.Canceled),
			want: true,
		},
		{
			name: "joined context.Canceled with context.Canceled",
			err:  errors.Join(context.Canceled, context.Canceled),
			want: true,
		},
		{
			name: "joined context.Canceled with wrapped context.Canceled",
			err:  errors.Join(context.Canceled, fmt.Errorf("wrap: %w", context.Canceled)),
			want: true,
		},
		{
			name: "joined context.Canceled with real io error",
			err:  errors.Join(context.Canceled, io.ErrUnexpectedEOF),
			want: false,
		},
		{
			name: "joined real error with context.Canceled",
			err:  errors.Join(errors.New("disk full"), context.Canceled),
			want: false,
		},
		{
			name: "pure io error",
			err:  io.ErrClosedPipe,
			want: false,
		},
		{
			name: "context deadline exceeded",
			err:  context.DeadlineExceeded,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := solelyCanceled(tt.err); got != tt.want {
				t.Errorf("solelyCanceled(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
