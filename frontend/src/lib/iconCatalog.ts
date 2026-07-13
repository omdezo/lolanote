// Board-icon catalog: monochrome Lucide glyphs (the same stroke style as the
// app's own icon set — white on the colored tile, exactly Milanote's look),
// tagged with keywords so the picker's search finds them ("timer" → timer,
// stopwatch, alarm, hourglass…). Letters & numbers render as typographic
// tiles from LETTER_ICONS. Icons persist as `content.icon = "<name>"`;
// legacy emoji values still render as text.
import type { LucideIcon } from 'lucide-react';
import {
  Activity, AlarmClock, AlertTriangle, Anchor, Aperture, Apple, Archive,
  Award, Baby, Backpack, BarChart3, Battery, Bed, Bell, Bike, Bird, Bone,
  Book, BookOpen, Bookmark, Box, Brain, Briefcase, Brush, Bug, Building,
  Building2, Bus, Cake, Calculator, Calendar, CalendarClock, Camera, Car,
  Cat, ChefHat, Cherry, Church, Clapperboard, Clock, Cloud, CloudRain,
  Code, Coffee, Cog, Coins, Compass, Cpu, CreditCard, Crown, Database,
  Diamond, Dog, DollarSign, Dumbbell, Egg, Eye, Feather, FileText, Film,
  Fish, Flag, FlaskConical, Flame, Flower2, Folder, FolderOpen, Footprints,
  Gamepad2, Gem, Gift, GitBranch, Glasses, Globe, GraduationCap, Guitar,
  Hammer, HandHeart, HardDrive, Headphones, Heart, HeartPulse, Home,
  Hourglass, IceCream, Image, Inbox, Key, Keyboard, Landmark, Laptop,
  Layers, Leaf, Library, Lightbulb, LineChart, Lock, Luggage, Mail,
  Map as MapGlyph,
  MapPin, Medal, Megaphone, MessageCircle, Mic, Microscope, Monitor, Moon,
  Mountain, Mouse, Music, Newspaper, Package, Paintbrush, Palette,
  Paperclip, PartyPopper, PenTool, Pencil, Phone, PieChart, PiggyBank,
  Pill, Pin, Pizza, Plane, Plug, Podcast, Puzzle, Rabbit, Radio, Rocket,
  Ruler, Salad, Scale, School, Scissors, Send, Server, Settings, Shield,
  Shirt, ShoppingBag, ShoppingCart, Smartphone, Snowflake, Sparkles,
  Speaker, Star, Stethoscope, Sun, Sunrise, Target, Tent, Terminal,
  TestTube, Timer, TrainFront, Trees, TrendingUp, Trophy, Truck, Tv,
  Umbrella, User, Users, Utensils, Video, Wallet, Wand2, Watch, Waves,
  Wifi, Wind, Wine, Wrench, Zap,
} from 'lucide-react';

export interface IconEntry {
  name: string;      // persisted in content.icon
  icon: LucideIcon;  // the glyph component
  k: string;         // space-separated keywords (lowercase)
}

const entry = (name: string, icon: LucideIcon, k: string): IconEntry => ({ name, icon, k: `${name} ${k}` });

export const ICON_CATALOG: IconEntry[] = [
  // time & planning
  entry('timer', Timer, 'stopwatch countdown time speed track'),
  entry('alarm', AlarmClock, 'alarm clock timer morning wake time'),
  entry('clock', Clock, 'time hour watch schedule'),
  entry('hourglass', Hourglass, 'timer sand time wait deadline'),
  entry('calendar', Calendar, 'date schedule plan month agenda'),
  entry('calendar-clock', CalendarClock, 'schedule deadline appointment time'),
  entry('watch', Watch, 'time wrist clock'),
  // work & documents
  entry('file', FileText, 'document page paper text notes'),
  entry('folder', Folder, 'files directory organize'),
  entry('folder-open', FolderOpen, 'files directory open project'),
  entry('archive', Archive, 'box storage old backup'),
  entry('briefcase', Briefcase, 'work business job office career'),
  entry('inbox', Inbox, 'mail tray tasks capture'),
  entry('book', Book, 'read study cover journal'),
  entry('book-open', BookOpen, 'read study pages learning'),
  entry('library', Library, 'books research collection study'),
  entry('bookmark', Bookmark, 'save favorite mark read'),
  entry('newspaper', Newspaper, 'news article press media'),
  entry('pencil', Pencil, 'write edit draft sketch'),
  entry('pen', PenTool, 'design vector draw write'),
  entry('paperclip', Paperclip, 'attach file clip'),
  entry('pin', Pin, 'location mark important tack'),
  entry('calculator', Calculator, 'math numbers accounting budget'),
  entry('chart', BarChart3, 'bar graph analytics data stats report'),
  entry('chart-line', LineChart, 'graph analytics trends data stats'),
  entry('chart-pie', PieChart, 'graph analytics share data stats'),
  entry('trending', TrendingUp, 'growth up arrow stats success'),
  entry('layers', Layers, 'stack design levels sheets'),
  // creative & media
  entry('palette', Palette, 'art paint design color creative'),
  entry('brush', Brush, 'paint art design creative'),
  entry('paintbrush', Paintbrush, 'paint art decorate creative'),
  entry('scissors', Scissors, 'cut craft trim'),
  entry('camera', Camera, 'photo picture photography shoot'),
  entry('aperture', Aperture, 'photo lens photography focus'),
  entry('image', Image, 'photo picture gallery'),
  entry('film', Film, 'movie cinema reel video footage'),
  entry('clapperboard', Clapperboard, 'film movie video cinema director scene shoot production'),
  entry('video', Video, 'camera movie record film youtube'),
  entry('tv', Tv, 'television screen show series watch'),
  entry('music', Music, 'note song melody audio sound'),
  entry('guitar', Guitar, 'music instrument rock band'),
  entry('headphones', Headphones, 'music audio listen podcast sound'),
  entry('mic', Mic, 'microphone sing voice record podcast'),
  entry('podcast', Podcast, 'audio show broadcast radio'),
  entry('radio', Radio, 'broadcast audio station music'),
  entry('speaker', Speaker, 'audio sound music volume'),
  entry('feather', Feather, 'write light quill poetry'),
  entry('wand', Wand2, 'magic wizard effects creative'),
  entry('sparkles', Sparkles, 'magic shine new special ai'),
  // tech
  entry('laptop', Laptop, 'computer code work tech dev'),
  entry('monitor', Monitor, 'desktop screen display tech'),
  entry('keyboard', Keyboard, 'typing keys tech'),
  entry('mouse', Mouse, 'computer click pointer tech'),
  entry('smartphone', Smartphone, 'phone mobile app device'),
  entry('cpu', Cpu, 'chip processor hardware tech computer'),
  entry('harddrive', HardDrive, 'storage disk data backup'),
  entry('database', Database, 'data storage sql records'),
  entry('server', Server, 'hosting backend infrastructure'),
  entry('code', Code, 'programming developer software brackets'),
  entry('terminal', Terminal, 'console command line shell code'),
  entry('git', GitBranch, 'version control branch code merge'),
  entry('bug', Bug, 'issue error debug insect'),
  entry('wifi', Wifi, 'internet network wireless connection'),
  entry('battery', Battery, 'power energy charge'),
  entry('plug', Plug, 'power electric connect energy'),
  entry('settings', Settings, 'gear config preferences options'),
  entry('cog', Cog, 'gear settings machine engineering'),
  entry('wrench', Wrench, 'tool fix repair maintenance'),
  entry('hammer', Hammer, 'build tool construction diy'),
  entry('lightbulb', Lightbulb, 'idea inspiration bright think innovation'),
  entry('brain', Brain, 'mind think smart ideas psychology memory'),
  entry('zap', Zap, 'lightning bolt fast energy power flash'),
  // science & learning
  entry('flask', FlaskConical, 'science lab chemistry experiment'),
  entry('test-tube', TestTube, 'science lab chemistry sample'),
  entry('microscope', Microscope, 'science lab research biology'),
  entry('graduation', GraduationCap, 'education school university degree study'),
  entry('school', School, 'education building study learning'),
  entry('eye', Eye, 'see watch vision view observe'),
  // travel & places
  entry('plane', Plane, 'airplane travel flight trip vacation'),
  entry('car', Car, 'drive vehicle road trip auto'),
  entry('bus', Bus, 'transit transport travel city'),
  entry('train', TrainFront, 'railway transit travel metro'),
  entry('rocket', Rocket, 'launch space startup fast ship'),
  entry('anchor', Anchor, 'ship sea boat marine harbor'),
  entry('bike', Bike, 'bicycle cycling exercise ride'),
  entry('map', MapGlyph, 'travel world navigation location plan'),
  entry('map-pin', MapPin, 'location place marker address'),
  entry('compass', Compass, 'navigation direction explore adventure'),
  entry('globe', Globe, 'world earth international planet web'),
  entry('luggage', Luggage, 'suitcase travel trip packing vacation'),
  entry('backpack', Backpack, 'bag school hiking travel'),
  entry('home', Home, 'house building living family'),
  entry('building', Building, 'office company city apartment'),
  entry('building2', Building2, 'office skyscraper company city'),
  entry('landmark', Landmark, 'bank museum government classic column'),
  entry('church', Church, 'religion worship building faith'),
  entry('tent', Tent, 'camping outdoor adventure'),
  entry('mountain', Mountain, 'peak hiking adventure nature summit'),
  entry('umbrella', Umbrella, 'rain weather beach protection'),
  // nature
  entry('leaf', Leaf, 'plant nature green eco garden'),
  entry('trees', Trees, 'forest nature park green wood'),
  entry('flower', Flower2, 'blossom spring nature garden'),
  entry('sun', Sun, 'sunny weather bright summer day light'),
  entry('sunrise', Sunrise, 'morning dawn early day start'),
  entry('moon', Moon, 'night crescent sleep dark ramadan'),
  entry('star', Star, 'favorite rating shine best'),
  entry('cloud', Cloud, 'weather sky storage'),
  entry('rain', CloudRain, 'weather storm water'),
  entry('snowflake', Snowflake, 'winter cold snow ice'),
  entry('wind', Wind, 'breeze weather air'),
  entry('waves', Waves, 'ocean sea water surf beach'),
  entry('flame', Flame, 'fire hot trending burn lit'),
  // animals
  entry('cat', Cat, 'kitten pet animal'),
  entry('dog', Dog, 'puppy pet animal'),
  entry('bird', Bird, 'fly animal tweet nature'),
  entry('fish', Fish, 'sea animal fishing aquarium'),
  entry('rabbit', Rabbit, 'bunny pet animal fast'),
  entry('bone', Bone, 'dog pet animal'),
  entry('footprints', Footprints, 'steps track walk trail'),
  // food & drink
  entry('utensils', Utensils, 'fork knife dinner restaurant meal food eat'),
  entry('chef', ChefHat, 'cooking kitchen recipe restaurant food'),
  entry('coffee', Coffee, 'cup drink cafe hot espresso morning'),
  entry('wine', Wine, 'glass drink celebration dinner'),
  entry('pizza', Pizza, 'food italian slice fast'),
  entry('salad', Salad, 'healthy food green diet vegetables'),
  entry('apple', Apple, 'fruit healthy food red'),
  entry('cherry', Cherry, 'fruit food sweet'),
  entry('egg', Egg, 'breakfast food cooking'),
  entry('cake', Cake, 'dessert sweet birthday celebration'),
  entry('ice-cream', IceCream, 'dessert sweet cold summer'),
  // health & sport
  entry('activity', Activity, 'pulse fitness health heartbeat exercise'),
  entry('heart-pulse', HeartPulse, 'health cardio medical fitness'),
  entry('dumbbell', Dumbbell, 'gym exercise fitness strength workout weights'),
  entry('stethoscope', Stethoscope, 'doctor health medical clinic'),
  entry('pill', Pill, 'medicine health pharmacy drug'),
  entry('bed', Bed, 'sleep rest hotel bedroom'),
  entry('gamepad', Gamepad2, 'game controller video gaming play'),
  entry('puzzle', Puzzle, 'piece jigsaw problem solving strategy'),
  // people & social
  entry('user', User, 'person profile account me'),
  entry('users', Users, 'people team group community friends'),
  entry('baby', Baby, 'child newborn family kids'),
  entry('hand-heart', HandHeart, 'care charity give love support'),
  entry('heart', Heart, 'love favorite like romance'),
  entry('message', MessageCircle, 'chat talk comment discussion'),
  entry('mail', Mail, 'email letter envelope message'),
  entry('send', Send, 'message paper plane share deliver'),
  entry('phone', Phone, 'call telephone contact'),
  entry('bell', Bell, 'notification alert reminder ring'),
  entry('megaphone', Megaphone, 'announce loud marketing broadcast'),
  // celebration & goals
  entry('party', PartyPopper, 'celebrate confetti birthday event tada'),
  entry('gift', Gift, 'present birthday surprise box'),
  entry('trophy', Trophy, 'winner champion award prize achievement'),
  entry('medal', Medal, 'award winner achievement honor'),
  entry('award', Award, 'ribbon achievement prize honor'),
  entry('target', Target, 'goal aim focus bullseye objective'),
  entry('flag', Flag, 'milestone marker country finish'),
  entry('crown', Crown, 'king queen royal vip best'),
  entry('gem', Gem, 'diamond jewel precious value premium'),
  entry('diamond', Diamond, 'jewel precious shape value'),
  // money & shopping
  entry('wallet', Wallet, 'money cash finance budget'),
  entry('credit-card', CreditCard, 'payment money finance bank'),
  entry('coins', Coins, 'money gold finance savings change'),
  entry('dollar', DollarSign, 'money price cost finance currency'),
  entry('piggy-bank', PiggyBank, 'savings money budget finance'),
  entry('shopping-cart', ShoppingCart, 'groceries buy store purchase'),
  entry('shopping-bag', ShoppingBag, 'buy store retail purchase fashion'),
  entry('package', Package, 'box delivery shipping parcel product'),
  entry('truck', Truck, 'delivery shipping transport logistics'),
  entry('box', Box, 'package storage container cube'),
  // security & misc
  entry('key', Key, 'unlock access password secret'),
  entry('lock', Lock, 'secure private password protected'),
  entry('shield', Shield, 'security protect safety defense'),
  entry('scale', Scale, 'balance law justice legal weigh'),
  entry('alert', AlertTriangle, 'warning caution danger attention important'),
  entry('shirt', Shirt, 'clothes fashion wardrobe style tshirt'),
  entry('glasses', Glasses, 'read vision style spectacles'),
  entry('ruler', Ruler, 'measure design length architect'),
];

const ICON_MAP = new Map(ICON_CATALOG.map((e) => [e.name, e.icon] as [string, LucideIcon]));

// iconByName resolves a persisted icon name to its glyph (undefined for
// letters and legacy emoji, which render as text).
export function iconByName(name: string): LucideIcon | undefined {
  return ICON_MAP.get(name);
}

// Letters & numbers tab: rendered as typographic tiles.
export const LETTER_ICONS: string[] = [
  ...'ABCDEFGHIJKLMNOPQRSTUVWXYZ'.split(''),
  ...'0123456789'.split(''),
  '&', '#', '@', '!', '?', '★', '№',
];

// searchIcons filters the catalog by all query terms.
export function searchIcons(query: string): IconEntry[] {
  const terms = query.trim().toLowerCase().split(/\s+/).filter(Boolean);
  if (terms.length === 0) return ICON_CATALOG;
  return ICON_CATALOG.filter((e) => terms.every((t) => e.k.includes(t)));
}

// isLetterIcon: single typographic character (renders as a letter tile).
export function isLetterIcon(icon: string): boolean {
  return icon.length <= 2 && /^[A-Z0-9&#@!?★№]{1,2}$/.test(icon);
}
