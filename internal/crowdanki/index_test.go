package crowdanki_test

import (
	"fmt"
	"strings"
	"testing"

	ca "github.com/FlandreXXX/anki-toolkit/internal/crowdanki"
)

func TestDefinitionDeclarations(t *testing.T) {
	first := `{"crowdanki_uuid":"` + modelID + `","name":"Модель","flds":[{"ord":0,"name":"Лицо","extra":{"a":1.00,"b":"ё"}}]}`
	same := `{"flds":[{"extra":{"b":"\u0451","a":1.00},"name":"Лицо","ord":0}],"name":"Модель","crowdanki_uuid":"` + modelID + `"}`
	firstConfig := `{"crowdanki_uuid":"` + configID + `","new":{"steps":[1,2],"ignored":false}}`
	sameConfig := `{"new":{"ignored":false,"steps":[1,2]},"crowdanki_uuid":"` + configID + `"}`
	for _, tc := range []struct {
		name, model, config string
		modelCode, cfgCode  ca.Code
	}{
		{"эквивалентные", same, sameConfig, "", ""},
		{"неизвестное значение поля", strings.Replace(same, "1.00", "1.01", 1), sameConfig, ca.Ambiguous, ""},
		{"числовая лексема", strings.Replace(same, "1.00", "1", 1), sameConfig, ca.Ambiguous, ""},
		{"порядок массива планировщика", same, strings.Replace(sameConfig, "[1,2]", "[2,1]", 1), "", ca.Ambiguous},
		{"отсутствие против null", same, strings.Replace(sameConfig, `"ignored":false,`, `"new_key":null,`, 1), "", ca.Ambiguous},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := parse(t, fmt.Sprintf(`{
				"__type__":"Deck","name":"Корень","note_models":[%s],"deck_configurations":[%s],
				"children":[{"name":"Подколода","deck_config_uuid":%q,"note_models":[%s],"deck_configurations":[%s],
				"notes":[{"guid":"GUID не UUID","note_model_uuid":%q,"fields":["строка"]}]}]}`,
				first, firstConfig, configID, tc.model, tc.config, modelID))
			notes, err := doc.FindNote("GUID не UUID")
			if err != nil {
				t.Fatal(err)
			}
			if models := doc.ModelDeclarations(modelID); len(models) != 2 {
				t.Fatalf("мест объявления модели: %d", len(models))
			} else {
				textValue(t, models[1].Deck.Name(), "Подколода")
			}
			if configs := doc.ConfigDeclarations(configID); len(configs) != 2 {
				t.Fatalf("мест объявления конфигурации: %d", len(configs))
			}
			model, err := doc.ResolveModel(notes.Entity)
			if tc.modelCode != "" {
				requireCode(t, err, tc.modelCode)
			} else if err != nil {
				t.Fatal(err)
			} else {
				textValue(t, model.Deck.Name(), "Корень")
			}
			cfg, err := doc.ResolveConfig(notes.Deck)
			if tc.cfgCode != "" {
				requireCode(t, err, tc.cfgCode)
			} else if err != nil {
				t.Fatal(err)
			} else {
				textValue(t, cfg.Deck.Name(), "Корень")
			}
		})
	}
}

func TestDuplicateNotesAndDecks(t *testing.T) {
	doc := parse(t, fmt.Sprintf(`{"__type__":"Deck","children":[
		{"crowdanki_uuid":%q,"notes":[{"guid":"дубль"}]},
		{"crowdanki_uuid":%q,"notes":[{"guid":"дубль"},{"guid":""}]}]}`, deckID, deckID))
	_, err := doc.FindDeck(deckID)
	requireCode(t, err, ca.Ambiguous)
	_, err = doc.FindNote("дубль")
	requireCode(t, err, ca.Ambiguous)
	_, err = doc.FindNote("")
	requireCode(t, err, ca.Invalid)
	if len(doc.DeckDeclarations(deckID)) != 2 || len(doc.NoteDeclarations("дубль")) != 2 {
		t.Fatal("потеряны объявления дубликатов")
	}
}

func TestBrokenRelations(t *testing.T) {
	const validField = `[{"name":"A","ord":0}]`
	const validValue = `["значение"]`
	const validRef = `,"note_model_uuid":"` + modelID + `"`
	testCases := []struct {
		name, fieldDefs, fieldValues, ref string
		code                              ca.Code
	}{
		{"отрицательный ord", `[{"name":"A","ord":-1}]`, validValue, validRef, ca.Invalid},
		{"дробный ord", `[{"name":"A","ord":0.1}]`, validValue, validRef, ca.Invalid},
		{"переполнение ord", `[{"name":"A","ord":9223372036854775808}]`, validValue, validRef, ca.Invalid},
		{"ord за границей", `[{"name":"A","ord":1}]`, validValue, validRef, ca.Invalid},
		{"ord отсутствует", `[{"name":"A"}]`, validValue, validRef, ca.Invalid},
		{"ord строка", `[{"name":"A","ord":"0"}]`, validValue, validRef, ca.Invalid},
		{"дубль ord", `[{"name":"A","ord":0},{"name":"B","ord":0}]`, `["x","y"]`, validRef, ca.Ambiguous},
		{"дубль имени", `[{"name":"A","ord":0},{"name":"A","ord":1}]`, `["x","y"]`, validRef, ca.Ambiguous},
		{"null вместо определения", `[null]`, validValue, validRef, ca.Invalid},
		{"нестроковое поле", validField, `[5]`, validRef, ca.Invalid},
		{"null вместо поля", validField, `[null]`, validRef, ca.Invalid},
		{"null вместо списка", validField, "null", validRef, ca.Invalid},
		{"длины списков", validField, "[]", validRef, ca.Invalid},
		{"отсутствующая ссылка", validField, validValue, "", ca.Missing},
		{"ссылка неправильного типа", validField, validValue, `,"note_model_uuid":false`, ca.Invalid},
		{"ссылка неправильной формы", validField, validValue, `,"note_model_uuid":"модель"`, ca.Invalid},
		{"объявление отсутствует", validField, validValue, `,"note_model_uuid":"` + clozeID + `"`, ca.Unresolved},
		{"неверный регистр идентичности", validField, validValue, `,"note_model_uuid":"` + strings.ToUpper(childID) + `"`, ca.Unresolved},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			doc := parse(t, fmt.Sprintf(`{"__type__":"Deck",
				"notes":[{"fields":%s%s}],"note_models":[{"crowdanki_uuid":%q,"flds":%s},
				{"crowdanki_uuid":%q,"flds":%s}]}`,
				tc.fieldValues, tc.ref, modelID, tc.fieldDefs, childID, validField))
			note, _ := doc.Root().Notes().At(0)
			_, err := doc.ResolveField(note, "A")
			requireCode(t, err, tc.code)
			_, err = doc.ResolveFieldOrd(note, 0)
			requireCode(t, err, tc.code)
		})
	}
	doc := parse(t, fixture)
	note, err := doc.FindNote("!:{♥}")
	if err != nil {
		t.Fatal(err)
	}
	for _, ord := range []int{-1, 2} {
		_, err := doc.ResolveFieldOrd(note.Entity, ord)
		requireCode(t, err, ca.Invalid)
	}
	_, err = doc.ResolveField(note.Entity, "несуществующее")
	requireCode(t, err, ca.Unresolved)
	_, err = doc.ResolveConfig(note.Deck)
	requireCode(t, err, ca.Missing)
}

func TestNestedDeclarationsAndWrongMarkers(t *testing.T) {
	doc := parse(t, fmt.Sprintf(`{"__type__":"Deck","deck_config_uuid":%q,
		"notes":[{"__type__":null,"guid":"повреждено","note_model_uuid":%q},
		         {"guid":"поддержано","note_model_uuid":%q}],
		"children":[{"note_models":[{"crowdanki_uuid":%q,"flds":[]},{"__type__":"DeckConfig","crowdanki_uuid":%q}],
		             "deck_configurations":[{"crowdanki_uuid":%q}]}]}`,
		configID, modelID, modelID, modelID, clozeID, configID))
	_, err := doc.FindNote("повреждено")
	requireCode(t, err, ca.Invalid)
	broken, _ := doc.Root().Notes().At(0)
	_, err = doc.ResolveModel(broken)
	requireCode(t, err, ca.Invalid)
	_, err = doc.FindModel(clozeID)
	requireCode(t, err, ca.Invalid)
	note, err := doc.FindNote("поддержано")
	if err != nil {
		t.Fatal(err)
	}
	model, err := doc.ResolveModel(note.Entity)
	if err != nil || model.Deck.Value().Offset() == doc.Root().Value().Offset() {
		t.Fatalf("вложенная модель недоступна: %v", err)
	}
	config, err := doc.ResolveConfig(doc.Root())
	if err != nil || config.Deck.Value().Offset() != model.Deck.Value().Offset() {
		t.Fatalf("вложенная конфигурация недоступна: %v", err)
	}
}
