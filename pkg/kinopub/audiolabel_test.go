package kinopub

import "testing"

// Подпись дорожки обязана РАЗЛИЧАТЬ. У части релизов kinopub отдаёт дорожки
// без студии и без вида, с lang="unk": если вернуть его, все дорожки
// подписаны одинаково и выбрать между ними нельзя.
func TestAudioTrackLabelDistinguishes(t *testing.T) {
	cases := []struct {
		name string
		in   AudioTrack
		want string
	}{
		{"студия важнее всего",
			AudioTrack{Author: &NamedRef{Title: "Кубик в Кубе"}, Type: NamedRef{Title: "дубляж"}, Codec: "ac3"},
			"Кубик в Кубе"},
		{"без студии — вид",
			AudioTrack{Type: NamedRef{Title: "многоголосый"}, Codec: "ac3", Channels: 6},
			"многоголосый"},
		{"без того и другого — формат",
			AudioTrack{Lang: "unk", Codec: "ac3", Channels: 6},
			"AC3 5.1"},
		{"только кодек",
			AudioTrack{Lang: "unk", Codec: "aac"},
			"AAC"},
		{"осмысленный язык лучше номера",
			AudioTrack{Lang: "rus"},
			"rus"},
		{"не осталось ничего — номер",
			AudioTrack{Lang: "unk", Index: 1},
			"#2"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.in.Label(); got != c.want {
				t.Fatalf("хотели %q, получили %q", c.want, got)
			}
		})
	}
}

// Две дорожки одного релиза не должны получить одинаковую подпись, если их
// вообще можно различить по данным площадки.
func TestAudioTrackLabelNeverUnknown(t *testing.T) {
	for _, a := range []AudioTrack{
		{Lang: "unk"},
		{Lang: ""},
		{Lang: "unk", Channels: 2},
	} {
		if got := a.Label(); got == "" || got == "unk" {
			t.Fatalf("бесполезная подпись %q для %+v", got, a)
		}
	}
}
