package notify

import (
	"testing"
	"time"
)

func TestNormalizeGameTitle(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"Epic Games (Game, PC) - Sorry We're Closed", "sorry we're closed"},
		{"Steam (Loot, PC) - Hunting Simulator 3 Pack", "hunting simulator 3 pack"},
		{"Flotsam", "flotsam"},
		{"   Flotsam   ", "flotsam"},
		{"FLOTSAM", "flotsam"},
	}

	for _, tc := range tests {
		got := normalizeGameTitle(tc.input)
		if got != tc.expected {
			t.Errorf("normalizeGameTitle(%q) = %q; want %q", tc.input, got, tc.expected)
		}
	}
}

func TestParseEpicPromotions(t *testing.T) {
	sampleJSON := []byte(`{
		"data": {
			"Catalog": {
				"searchStore": {
					"elements": [
						{
							"id": "3be0a0d8479946d78f60d90c9442a0b4",
							"title": "Flotsam",
							"description": "The floating garbage town survival game!",
							"offerMappings": [
								{
									"pageSlug": "flotsam-266287",
									"pageType": "productHome"
								}
							],
							"price": {
								"totalPrice": {
									"discountPrice": 0,
									"originalPrice": 2499
								}
							},
							"promotions": {
								"promotionalOffers": [
									{
										"promotionalOffers": [
											{
												"startDate": "2026-11-05T16:00:00.000Z",
												"endDate": "2026-11-12T16:00:00.000Z",
												"discountSetting": {
													"discountType": "PERCENTAGE",
													"discountPercentage": 0
												}
											}
										]
									}
								]
							}
						},
						{
							"id": "paid-game-123",
							"title": "Paid Game",
							"description": "Not free",
							"price": {
								"totalPrice": {
									"discountPrice": 1999,
									"originalPrice": 1999
								}
							},
							"promotions": null
						},
						{
							"id": "expired-game-456",
							"title": "Expired Promo",
							"description": "Was free last month",
							"price": {
								"totalPrice": {
									"discountPrice": 0,
									"originalPrice": 1500
								}
							},
							"promotions": {
								"promotionalOffers": [
									{
										"promotionalOffers": [
											{
												"startDate": "2026-10-01T16:00:00.000Z",
												"endDate": "2026-10-08T16:00:00.000Z",
												"discountSetting": {
													"discountType": "PERCENTAGE",
													"discountPercentage": 0
												}
											}
										]
									}
								]
							}
						}
					]
				}
			}
		}
	}`)

	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	items := parseEpicPromotions(sampleJSON, now)

	if len(items) != 1 {
		t.Fatalf("expected 1 free game, got %d", len(items))
	}

	item := items[0]
	if item.Title != "Flotsam" {
		t.Errorf("expected Title 'Flotsam', got %q", item.Title)
	}
	if item.GUID != "epic:3be0a0d8479946d78f60d90c9442a0b4" {
		t.Errorf("expected GUID 'epic:3be0a0d8479946d78f60d90c9442a0b4', got %q", item.GUID)
	}
	if item.Link != "https://store.epicgames.com/p/flotsam-266287" {
		t.Errorf("expected Link 'https://store.epicgames.com/p/flotsam-266287', got %q", item.Link)
	}
	if item.Source != "Epic Games" {
		t.Errorf("expected Source 'Epic Games', got %q", item.Source)
	}
}

func TestParseFeedEntriesFiltersEpicAndAmazon(t *testing.T) {
	sampleFeed := `<?xml version="1.0" encoding="utf-8"?>
	<feed xmlns="http://www.w3.org/2005/Atom">
		<entry>
			<id>steam-item-1</id>
			<title>Steam (Game, PC) - Cool Steam Game</title>
			<link href="https://store.steampowered.com/app/12345/CoolGame/"/>
			<category term="source:Steam"/>
			<content>Free weekend</content>
		</entry>
		<entry>
			<id>amazon-item-2</id>
			<title>Amazon Prime (Game, PC) - Some Prime Game</title>
			<link href="https://gaming.amazon.com/loot"/>
			<category term="source:Amazon Prime"/>
			<content>Prime loot</content>
		</entry>
		<entry>
			<id>epic-item-3</id>
			<title>Epic Games (Game, PC) - Stale Epic Game</title>
			<link href="https://store.epicgames.com/p/stale-game"/>
			<category term="source:Epic Games"/>
			<content>Free game</content>
		</entry>
	</feed>`

	items := parseFeedEntries(sampleFeed)
	if len(items) != 1 {
		t.Fatalf("expected 1 item (Steam only), got %d", len(items))
	}
	if items[0].Title != "Steam (Game, PC) - Cool Steam Game" {
		t.Errorf("unexpected item title: %q", items[0].Title)
	}
}
