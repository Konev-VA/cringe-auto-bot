// Package listing описывает объявление о продаже автомобиля и правила
// распознавания ссылок на такие объявления.
package listing

import "strings"

// Listing — разобранное объявление.
type Listing struct {
	SourceURL   string
	Title       string
	Price       string
	City        string
	Description string
	Specs       []Spec   // характеристики в порядке отображения
	Photos      []string // абсолютные URL, ≤10
}

// Spec — одна характеристика вида "Год выпуска: 1986".
type Spec struct{ Key, Value string }

// OrderSpecs стабильно переставляет характеристики согласно priority.
// Ключи, которых нет в priority, сохраняют исходный относительный порядок
// и идут после известных.
func OrderSpecs(specs []Spec, priority []string) []Spec {
	if len(specs) == 0 {
		return nil
	}

	ordered := make([]Spec, 0, len(specs))
	used := make([]bool, len(specs))
	for _, key := range priority {
		for i, s := range specs {
			if !used[i] && s.Key == key {
				ordered = append(ordered, s)
				used[i] = true
				break
			}
		}
	}
	for i, s := range specs {
		if !used[i] {
			ordered = append(ordered, s)
		}
	}

	return ordered
}

// TrimSpecs оставляет не больше limit характеристик, сохраняя порядок.
// Ключи из required остаются в любом случае, остальные отбрасываются с конца.
func TrimSpecs(specs []Spec, limit int, required []string) []Spec {
	if limit <= 0 || len(specs) <= limit {
		return specs
	}

	keep := make([]bool, len(specs))
	n := 0
	for _, key := range required {
		for i, s := range specs {
			if keep[i] || s.Key != key {
				continue
			}
			keep[i] = true
			n++
			break
		}
	}
	for i := range specs {
		if n >= limit {
			break
		}
		if !keep[i] {
			keep[i] = true
			n++
		}
	}

	trimmed := make([]Spec, 0, n)
	for i, s := range specs {
		if keep[i] {
			trimmed = append(trimmed, s)
		}
	}
	return trimmed
}

// JoinNonEmpty склеивает части через sep, опуская пустые.
func JoinNonEmpty(sep string, parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}
