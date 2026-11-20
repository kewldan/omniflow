package payments

import "testing"

func TestOptionKeysRoundTrip(t *testing.T) {
	t.Parallel()
	if key := OptionKey("freekassa", "sbp"); key != "freekassa.sbp" {
		t.Fatalf("key = %q", key)
	}
	if key := OptionKey("yookassa", ""); key != "yookassa" {
		t.Fatalf("a default method keeps the bare slug, got %q", key)
	}
	if provider, method := SplitOptionKey("kassa_ai.sberpay"); provider != "kassa_ai" || method != "sberpay" {
		t.Fatalf("split = %q %q", provider, method)
	}
	if ValidMethodKey("toolongmethodname") || ValidMethodKey("SBP") || !ValidMethodKey("sbp") || !ValidMethodKey("") {
		t.Fatal("method key validation")
	}
}

func TestParseMethodCodes(t *testing.T) {
	t.Parallel()
	order, ids, err := parseMethodIDs("X", "sbp, card , qr:42", map[string]int{"sbp": 44, "card": 36})
	if err != nil {
		t.Fatal(err)
	}
	if len(order) != 3 || order[0] != "sbp" || ids["sbp"] != 44 || ids["card"] != 36 || ids["qr"] != 42 {
		t.Fatalf("order=%v ids=%v", order, ids)
	}
	for _, bad := range []string{"unknown", "sbp,sbp", "qr:0", "qr:abc", "Bad Key:1"} {
		if _, _, err := parseMethodIDs("X", bad, map[string]int{"sbp": 44}); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
	if order, _, err := parseMethodCodes("X", "", nil); err != nil || order != nil {
		t.Fatalf("an empty list is no methods: %v %v", order, err)
	}
}

// FreeKassa with methods sends each button's payment system.
func TestFreeKassaMethodsPickThePaymentSystem(t *testing.T) {
	t.Parallel()
	provider, err := buildFreeKassa(Settings{Env: func(key string) string {
		return map[string]string{
			"APP_FREEKASSA_SHOP_ID": "1", "APP_FREEKASSA_API_KEY": "k", "APP_FREEKASSA_SECRET_WORD_1": "a",
			"APP_FREEKASSA_SECRET_WORD_2": "b", "APP_FREEKASSA_SERVER_IP": "203.0.113.1",
			"APP_FREEKASSA_PAYMENT_METHODS": "sbp,card",
		}[key]
	}})
	if err != nil {
		t.Fatal(err)
	}
	freekassa := provider.(*FreeKassa)
	if methods := ProviderMethods(freekassa); len(methods) != 2 || methods[1] != "card" {
		t.Fatalf("methods = %v", methods)
	}
	if !freekassa.useAPI {
		t.Fatal("methods need the order API")
	}
	if freekassa.core.systemFor("sbp") != 44 || freekassa.core.systemFor("card") != 36 || freekassa.core.systemFor("") != 1 {
		t.Fatalf("systems = %d %d %d", freekassa.core.systemFor("sbp"), freekassa.core.systemFor("card"), freekassa.core.systemFor(""))
	}
}
