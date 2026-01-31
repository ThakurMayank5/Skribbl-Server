package main

import "math/rand"

func getRandomWords(count int) []string {
	shuffled := make([]string, len(Words))
	copy(shuffled, Words)

	rand.Shuffle(len(shuffled), func(i, j int) {
		shuffled[i], shuffled[j] = shuffled[j], shuffled[i]
	})

	return shuffled[:count]
}

func generateHint(word string) string {
	hint := ""
	for i, char := range word {
		if i == 0 || i == len(word)-1 {
			hint += string(char)
		} else {
			hint += "_"
		}
	}
	return hint
}
