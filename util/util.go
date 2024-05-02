package util

import (
	"math/rand"
	"slices"
)

var letters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890")

type Scale []int

var Scales = map[string]Scale{
	"fibonacci":   {1, 2, 3, 5, 8, 13, 21, 34, 55, 89, 144},
	"workingdays": {1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14},
}

func RandSeq(n int) string {
	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

func Average(s []int) float64 {
	average := 0.0
	for _, v := range s {
		average += float64(v)
	}
	return average / float64(len(s))
}

func Median(s []int) float64 {
	sCopy := make([]int, len(s))
	copy(sCopy, s)

	slices.Sort(sCopy)

	l := len(sCopy)
	if l == 0 {
		return 0
	} else if l%2 == 0 {
		return Average(sCopy[l/2-1 : l/2+1])
	} else {
		return float64(sCopy[l/2])
	}
}

func Recommendation(av float64, med float64, scale Scale) int {
	a := (av + med) / 2
	for _, entry := range scale {
		if float64(entry) >= a {
			return entry
		}
	}
	return scale[len(scale)-1]
}
