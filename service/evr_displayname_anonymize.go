package service

import (
	"math/rand"
)

var nameComponents = struct {
	Adjectives []string
	Nouns      []string
}{
	Adjectives: []string{
		"Accidental", "Admiral", "Alpha", "Angry", "Applauding", "Approving", "Artsy", "Awesome", "Bashful", "Basic", "Bitter", "Bookish", "Boss", "Brainy", "Brash", "Brave", "Bronze", "Bubbly", "Burpy", "Captain", "Careful", "Caring", "Chatty", "Chef", "Chief", "Classic", "Clever", "Clumsy", "Cold", "Colonel", "Commanding", "Confessing", "Confident", "Conflicted", "Confused", "Cool", "Courageous", "Coveted", "Cowardly", "Cranky", "Creepy", "Cruel", "Crunchy", "Curious", "Cute", "Cyan", "Cyber", "Dapper", "Demanding", "Deputized", "Diamond", "Digi", "Distinct", "Distinguished", "Emerald", "Expectant", "Extra", "Fainting", "Familiar", "Fast", "Fearful", "Fearless", "First", "Flaky", "Flourishing", "Flying", "Forgetful", "Friendly", "Frightened", "Frigid", "Fun", "General", "Generous", "Gentle", "Ghost", "Ghostly", "Gold", "Goofy", "Gravity", "Greedy", "Green", "Grumpy", "Guilty", "Happy", "Harmonious", "Haunted", "Haunting", "Helpful", "Imaginative", "Jealous", "Kind", "Last", "Leaping", "Likable", "Lil", "Little", "Lone", "Lovable", "Lumbering", "Lying", "Magical", "Major", "Mango", "Marbled", "Mean", "Mecha", "Meddlesome", "Meek", "Mega", "Metal", "Mini", "Nebulous", "Needy", "Nervous", "Nice", "Nifty", "Old", "Orange", "Outgoing", "Pepped", "Perfect", "Picturesque", "Plain", "Platinum", "Playful", "Poised", "Ponderous", "Prestigious", "Prideful", "Private", "Proper", "Punctual", "Purple", "Quaint", "Quick", "Rad", "Raging", "Ravenous", "Respectful", "Robo", "Ruby", "Rusty", "Sad", "Sapphire", "Scared", "Scary", "Secretive", "Sensitive", "Sergeant", "Shady", "Sharp", "Shy", "Silly", "Silver", "Sincere", "Skillful", "Skulking", "Sleepy", "Sly", "Smiling", "Sneaky", "Sneezy", "Snuggly", "Sour", "Space", "Spicy", "Spiteful", "Spoiled", "Spring", "Spurious", "Steel", "Strong", "Studious", "Stunning", "Sturdy", "Sulking", "Super", "Tame", "Tango", "Terrible", "Tilted", "Tiny", "Toasty", "Tropical", "Troubled", "Troubling", "Turquoise", "Twisted", "Typical", "Uber", "UltraMega", "Vain", "Virtuous", "Wheezing", "Wheezy", "Wise", "Tenacity",
	},
	Nouns: []string{
		"Alien", "Android", "Anemone", "Angel", "Badger", "Bandit", "Bear", "Biscuit", "Blanket", "Boar", "Boat", "Bot", "Brawler", "Camper", "Cat", "Chicken", "Chupacabra", "Chupathingy", "Clown", "Comet", "Confidant", "Cyclone", "Daisy", "Defender", "Demon", "Dog", "Doge", "Doggo", "Doll", "Dolphin", "Elephant", "Elk", "Flounder", "Follower", "Fox", "Fridge", "Frog", "Galahad", "Giant", "Goat", "Gunner", "Hamster", "Hero", "Hunter", "Isabeau", "Jack", "King", "Kitten", "Knight", "Lafayette", "Leader", "Lightning", "Lily", "Lion", "Liv", "Llama", "Machine", "Mallory", "Mannequin", "Medic", "Meerkat", "Mercenary", "Meteor", "Moose", "Moss", "Nebula", "Nova", "Oak", "Orange", "Panda", "Penguin", "Poet", "Prince", "Princess", "Puma", "Puncher", "Pupper", "Puppy", "Quasar", "Queen", "Rabbit", "Rain", "Robot", "Rose", "Scanner", "Scout", "Shark", "Ship", "Snail", "Sniper", "Spork", "Squirrel", "Star", "Stunner", "Thunder", "Toast", "Toaster", "TrashPanda", "Turnip", "Villain", "Vortex", "Weasel", "Wolf", "Boba", "Buck", "Cheeeee", "CloudClone", "David", "DreamWeaver", "Eoraptor", "Flower", "HammelTime", "Kaizer", "Kong", "Pancake", "Potato", "Pipsicles", "Qberry", "QTPie", "Sakura", "SpaceMutt", "SpeclDelvry", "Tyranna", "Luis", "Winner", "Zebra",
	},
}

func RandomDisplayName() string {
	// Select a random adjective and noun
	adjective := nameComponents.Adjectives[rand.Intn(len(nameComponents.Adjectives))]
	noun := nameComponents.Nouns[rand.Intn(len(nameComponents.Nouns))]

	// Combine the adjective and noun
	return adjective + noun
}
