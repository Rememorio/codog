package toolnames

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSuggestionsRanksLikelyToolNames(t *testing.T) {
	require.Equal(t, []string{"bash"}, Suggestions("Bsh", []string{"read_file", "bash", "write_file"}, 4))
	require.Equal(t, []string{"read_file"}, Suggestions("ReadFile", []string{"read_file", "write_file"}, 4))
}

func TestSuggestionsHonorsLimitAndDeduplicates(t *testing.T) {
	got := Suggestions("writefile", []string{"write_file", "write_file", "read_file"}, 1)

	require.Equal(t, []string{"write_file"}, got)
}
