// Lightweight i18n: a flat key → string dictionary per language, a plain t()
// for non-React code, and a useT() hook that re-renders on language change.
// The board canvas keeps LTR geometry in both languages (positions are
// absolute canvas coordinates); text simply renders in the chosen language.
import type { Language, LocalizationSettings } from '../api/types';

const en = {
  // chrome
  'app.home': 'Home',
  'app.untitled': 'Untitled',
  'topbar.boards': 'Boards',
  'topbar.search': 'Search',
  'topbar.unsorted': 'Unsorted',
  'topbar.trash': 'Trash',
  'topbar.templates': 'Templates',
  'topbar.export': 'Export as Markdown',
  'topbar.share': 'Share',
  'topbar.undo': 'Undo',
  'topbar.redo': 'Redo',
  'topbar.settings': 'Settings',
  'topbar.logout': 'Log out',
  'topbar.homeNoExport': 'The Home board cannot be exported',
  'topbar.homeNoShare': 'The Home board can never be shared',
  'topbar.shareThis': 'Share this board',

  // toolbar
  'tool.note': 'Note', 'tool.link': 'Link', 'tool.todo': 'To-do',
  'tool.line': 'Line', 'tool.board': 'Board', 'tool.column': 'Column',
  'tool.comment': 'Comment', 'tool.table': 'Table', 'tool.sketch': 'Sketch',
  'tool.color': 'Color', 'tool.document': 'Document', 'tool.audio': 'Audio',
  'tool.map': 'Map', 'tool.video': 'Video', 'tool.heading': 'Heading',
  'tool.image': 'Add image', 'tool.upload': 'Upload', 'tool.draw': 'Draw',
  'tool.more': 'More', 'tool.trashHint': 'Drag cards here to delete',

  // settings dialog
  'settings.title': 'Settings',
  'settings.account': 'Account settings',
  'settings.notifications': 'Emails & notifications',
  'settings.appearance': 'Appearance',
  'settings.preferences': 'Preferences',
  'settings.localization': 'Localization',
  'settings.toolbar': 'Toolbar options',
  'settings.privacy': 'Privacy',

  'account.name': 'Name', 'account.email': 'Email', 'account.password': 'Password',
  'account.change': 'Change', 'account.reset': 'Reset', 'account.plan': 'Plan',
  'account.dangerZone': 'Danger zone!',
  'account.deleteAccount': 'Delete your account',
  'account.deleteWarning': 'This permanently deletes your account, every board you own, and all uploaded files. This cannot be undone.',
  'account.deleteConfirmType': 'Type DELETE to confirm',
  'account.deleteForever': 'Delete forever',
  'account.currentPassword': 'Current password',
  'account.newPassword': 'New password (min. 8 characters)',
  'account.passwordChanged': 'Password changed',
  'account.passwordFailed': 'Password change failed — check your current password',
  'account.nameSaved': 'Name updated',
  'account.emailSaved': 'Email updated — use it on your next sign-in',
  'account.save': 'Save', 'account.cancel': 'Cancel',
  'account.newName': 'Your name', 'account.newEmail': 'New email address',

  'notif.inApp': 'In-app notifications',
  'notif.mentions': 'Mentions', 'notif.mentionsSub': 'When someone @mentions you in a comment',
  'notif.comments': 'Comments', 'notif.commentsSub': 'When someone comments on your boards',
  'notif.shares': 'Sharing', 'notif.sharesSub': 'When a board is shared with you',
  'notif.assignments': 'Task assignments', 'notif.assignmentsSub': 'When a task is assigned to you',
  'notif.boardChanges': 'Board activity', 'notif.boardChangesSub': 'Edits made to boards you follow',
  'notif.reminders': 'Task reminders', 'notif.remindersSub': 'When a task reminder is due',
  'notif.email': 'Email',
  'notif.emailEnabled': 'Email notifications', 'notif.emailEnabledSub': 'Also deliver notifications to your inbox',
  'notif.digest': 'Summary digest', 'notif.digestSub': 'A periodic roundup of activity',
  'notif.digest.off': 'Off', 'notif.digest.daily': 'Daily', 'notif.digest.weekly': 'Weekly',

  'appearance.theme': 'Theme',
  'appearance.theme.light': 'Light', 'appearance.theme.dark': 'Dark', 'appearance.theme.system': 'System',
  'appearance.accent': 'Accent color',
  'appearance.dotGrid': 'Canvas dot grid', 'appearance.dotGridSub': 'Show the dotted background on boards',
  'appearance.cardShadows': 'Card shadows', 'appearance.cardShadowsSub': 'Soft drop shadows under cards',
  'appearance.density': 'Interface density',
  'appearance.density.comfortable': 'Comfortable', 'appearance.density.compact': 'Compact',

  'prefs.doubleClick': 'Double-click on the canvas creates',
  'prefs.doubleClick.note': 'A note', 'prefs.doubleClick.board': 'A board', 'prefs.doubleClick.none': 'Nothing',
  'prefs.wheel': 'Mouse wheel',
  'prefs.wheel.pan': 'Scrolls the canvas', 'prefs.wheel.zoom': 'Zooms the canvas',
  'prefs.wheelSub': 'Ctrl+wheel always zooms; trackpads pinch to zoom',
  'prefs.snap': 'Snap to grid', 'prefs.snapSub': 'Align cards to a 20px grid when dropping',
  'prefs.spell': 'Spell check', 'prefs.spellSub': 'Underline misspelled words while editing',
  'prefs.hints': 'Canvas hints', 'prefs.hintsSub': 'Show the helper pill on empty boards',

  'loc.language': 'Language',
  'loc.firstDay': 'First day of the week',
  'loc.day.sunday': 'Sunday', 'loc.day.monday': 'Monday', 'loc.day.saturday': 'Saturday',
  'loc.dateFormat': 'Date format',
  'loc.dateFormat.auto': 'Automatic',
  'loc.timeFormat': 'Time format',
  'loc.timeFormat.12h': '12-hour', 'loc.timeFormat.24h': '24-hour',

  'toolbaropts.hint': 'Choose which tools appear in the toolbar. Hidden tools stay available through menus and shortcuts.',

  'privacy.presence': 'Show me in presence', 'privacy.presenceSub': 'Let collaborators see when you are viewing a board, plus your live cursor',
  'privacy.email': 'Show my email to collaborators', 'privacy.emailSub': 'People you share boards with can see your email address',
  'privacy.export': 'Download my data', 'privacy.exportSub': 'Everything you own — boards, cards, labels — as a single JSON file',
  'privacy.download': 'Download',

  'common.on': 'On', 'common.off': 'Off',
  'common.saving': 'Saving…', 'common.saved': 'All changes saved',
  'common.close': 'Close',
};

type Dict = typeof en;
export type TKey = keyof Dict;

const ar: Record<TKey, string> = {
  'app.home': 'الرئيسية',
  'app.untitled': 'بدون عنوان',
  'topbar.boards': 'اللوحات',
  'topbar.search': 'بحث',
  'topbar.unsorted': 'غير مصنّف',
  'topbar.trash': 'المهملات',
  'topbar.templates': 'القوالب',
  'topbar.export': 'تصدير Markdown',
  'topbar.share': 'مشاركة',
  'topbar.undo': 'تراجع',
  'topbar.redo': 'إعادة',
  'topbar.settings': 'الإعدادات',
  'topbar.logout': 'تسجيل الخروج',
  'topbar.homeNoExport': 'لا يمكن تصدير اللوحة الرئيسية',
  'topbar.homeNoShare': 'لا يمكن مشاركة اللوحة الرئيسية أبداً',
  'topbar.shareThis': 'شارك هذه اللوحة',

  'tool.note': 'ملاحظة', 'tool.link': 'رابط', 'tool.todo': 'مهام',
  'tool.line': 'خط', 'tool.board': 'لوحة', 'tool.column': 'عمود',
  'tool.comment': 'تعليق', 'tool.table': 'جدول', 'tool.sketch': 'رسم',
  'tool.color': 'لون', 'tool.document': 'مستند', 'tool.audio': 'صوت',
  'tool.map': 'خريطة', 'tool.video': 'فيديو', 'tool.heading': 'عنوان',
  'tool.image': 'إضافة صورة', 'tool.upload': 'رفع ملف', 'tool.draw': 'رسم حر',
  'tool.more': 'المزيد', 'tool.trashHint': 'اسحب البطاقات هنا للحذف',

  'settings.title': 'الإعدادات',
  'settings.account': 'إعدادات الحساب',
  'settings.notifications': 'البريد والإشعارات',
  'settings.appearance': 'المظهر',
  'settings.preferences': 'التفضيلات',
  'settings.localization': 'اللغة والمنطقة',
  'settings.toolbar': 'خيارات شريط الأدوات',
  'settings.privacy': 'الخصوصية',

  'account.name': 'الاسم', 'account.email': 'البريد الإلكتروني', 'account.password': 'كلمة المرور',
  'account.change': 'تغيير', 'account.reset': 'إعادة تعيين', 'account.plan': 'الخطة',
  'account.dangerZone': 'منطقة الخطر!',
  'account.deleteAccount': 'حذف حسابك',
  'account.deleteWarning': 'سيؤدي هذا إلى حذف حسابك نهائياً مع كل اللوحات التي تملكها وجميع الملفات المرفوعة. لا يمكن التراجع عن هذا.',
  'account.deleteConfirmType': 'اكتب DELETE للتأكيد',
  'account.deleteForever': 'حذف نهائي',
  'account.currentPassword': 'كلمة المرور الحالية',
  'account.newPassword': 'كلمة المرور الجديدة (٨ أحرف على الأقل)',
  'account.passwordChanged': 'تم تغيير كلمة المرور',
  'account.passwordFailed': 'فشل تغيير كلمة المرور — تحقق من كلمة المرور الحالية',
  'account.nameSaved': 'تم تحديث الاسم',
  'account.emailSaved': 'تم تحديث البريد — استخدمه عند تسجيل الدخول القادم',
  'account.save': 'حفظ', 'account.cancel': 'إلغاء',
  'account.newName': 'اسمك', 'account.newEmail': 'البريد الإلكتروني الجديد',

  'notif.inApp': 'إشعارات داخل التطبيق',
  'notif.mentions': 'الإشارات', 'notif.mentionsSub': 'عندما يشير إليك أحد في تعليق',
  'notif.comments': 'التعليقات', 'notif.commentsSub': 'عندما يعلّق أحد على لوحاتك',
  'notif.shares': 'المشاركة', 'notif.sharesSub': 'عندما تُشارك لوحة معك',
  'notif.assignments': 'إسناد المهام', 'notif.assignmentsSub': 'عندما تُسند إليك مهمة',
  'notif.boardChanges': 'نشاط اللوحات', 'notif.boardChangesSub': 'تعديلات على اللوحات التي تتابعها',
  'notif.reminders': 'تذكيرات المهام', 'notif.remindersSub': 'عند حلول موعد تذكير مهمة',
  'notif.email': 'البريد الإلكتروني',
  'notif.emailEnabled': 'إشعارات البريد', 'notif.emailEnabledSub': 'إرسال الإشعارات إلى بريدك أيضاً',
  'notif.digest': 'ملخص دوري', 'notif.digestSub': 'خلاصة دورية للنشاط',
  'notif.digest.off': 'إيقاف', 'notif.digest.daily': 'يومي', 'notif.digest.weekly': 'أسبوعي',

  'appearance.theme': 'السمة',
  'appearance.theme.light': 'فاتح', 'appearance.theme.dark': 'داكن', 'appearance.theme.system': 'النظام',
  'appearance.accent': 'اللون المميز',
  'appearance.dotGrid': 'شبكة النقاط', 'appearance.dotGridSub': 'إظهار الخلفية المنقطة على اللوحات',
  'appearance.cardShadows': 'ظلال البطاقات', 'appearance.cardShadowsSub': 'ظلال ناعمة أسفل البطاقات',
  'appearance.density': 'كثافة الواجهة',
  'appearance.density.comfortable': 'مريح', 'appearance.density.compact': 'مضغوط',

  'prefs.doubleClick': 'النقر المزدوج على اللوحة ينشئ',
  'prefs.doubleClick.note': 'ملاحظة', 'prefs.doubleClick.board': 'لوحة', 'prefs.doubleClick.none': 'لا شيء',
  'prefs.wheel': 'عجلة الفأرة',
  'prefs.wheel.pan': 'تحرّك اللوحة', 'prefs.wheel.zoom': 'تكبّر وتصغّر',
  'prefs.wheelSub': 'Ctrl+العجلة يكبّر دائماً؛ لوحات اللمس تستخدم القرص',
  'prefs.snap': 'محاذاة إلى الشبكة', 'prefs.snapSub': 'محاذاة البطاقات إلى شبكة 20 بكسل عند الإفلات',
  'prefs.spell': 'التدقيق الإملائي', 'prefs.spellSub': 'تسطير الكلمات الخاطئة أثناء الكتابة',
  'prefs.hints': 'تلميحات اللوحة', 'prefs.hintsSub': 'إظهار شريط المساعدة على اللوحات الفارغة',

  'loc.language': 'اللغة',
  'loc.firstDay': 'أول أيام الأسبوع',
  'loc.day.sunday': 'الأحد', 'loc.day.monday': 'الاثنين', 'loc.day.saturday': 'السبت',
  'loc.dateFormat': 'تنسيق التاريخ',
  'loc.dateFormat.auto': 'تلقائي',
  'loc.timeFormat': 'تنسيق الوقت',
  'loc.timeFormat.12h': '١٢ ساعة', 'loc.timeFormat.24h': '٢٤ ساعة',

  'toolbaropts.hint': 'اختر الأدوات التي تظهر في شريط الأدوات. تبقى الأدوات المخفية متاحة عبر القوائم والاختصارات.',

  'privacy.presence': 'إظهار حضوري', 'privacy.presenceSub': 'السماح للمتعاونين برؤية تواجدك على اللوحة ومؤشرك المباشر',
  'privacy.email': 'إظهار بريدي للمتعاونين', 'privacy.emailSub': 'يمكن لمن تشاركهم اللوحات رؤية بريدك الإلكتروني',
  'privacy.export': 'تنزيل بياناتي', 'privacy.exportSub': 'كل ما تملكه — لوحات وبطاقات ووسوم — في ملف JSON واحد',
  'privacy.download': 'تنزيل',

  'common.on': 'مفعّل', 'common.off': 'معطّل',
  'common.saving': 'جارٍ الحفظ…', 'common.saved': 'تم حفظ كل التغييرات',
  'common.close': 'إغلاق',
};

const dicts: Record<Language, Record<TKey, string>> = { en, ar };

let currentLanguage: Language = 'en';

export function setLanguage(lang: Language) {
  currentLanguage = lang;
  document.documentElement.setAttribute('lang', lang);
}

export function getLanguage(): Language {
  return currentLanguage;
}

// t translates a key in the current language, falling back to English.
export function t(key: TKey): string {
  return dicts[currentLanguage][key] ?? en[key] ?? key;
}

// ---- date & time formatting honoring the localization settings ----

const localeFor: Record<Language, string> = { en: 'en-US', ar: 'ar' };

export function formatDate(iso: string | Date, loc: LocalizationSettings): string {
  const d = typeof iso === 'string' ? new Date(iso) : iso;
  if (Number.isNaN(d.getTime())) return '';
  if (loc.dateFormat === 'auto') {
    return d.toLocaleDateString(localeFor[loc.language], { year: 'numeric', month: 'short', day: 'numeric' });
  }
  const dd = String(d.getDate()).padStart(2, '0');
  const mm = String(d.getMonth() + 1).padStart(2, '0');
  const yyyy = d.getFullYear();
  switch (loc.dateFormat) {
    case 'dmy': return `${dd}/${mm}/${yyyy}`;
    case 'mdy': return `${mm}/${dd}/${yyyy}`;
    case 'ymd': return `${yyyy}-${mm}-${dd}`;
    default: return d.toLocaleDateString();
  }
}

export function formatTime(iso: string | Date, loc: LocalizationSettings): string {
  const d = typeof iso === 'string' ? new Date(iso) : iso;
  if (Number.isNaN(d.getTime())) return '';
  return d.toLocaleTimeString(localeFor[loc.language], {
    hour: 'numeric', minute: '2-digit', hour12: loc.timeFormat === '12h',
  });
}

// relativeTime renders "5m ago"-style stamps for notification rows.
export function relativeTime(iso: string, loc: LocalizationSettings): string {
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return '';
  const mins = Math.round((Date.now() - then) / 60_000);
  const rtf = new Intl.RelativeTimeFormat(localeFor[loc.language], { numeric: 'auto' });
  if (Math.abs(mins) < 60) return rtf.format(-mins, 'minute');
  const hours = Math.round(mins / 60);
  if (Math.abs(hours) < 24) return rtf.format(-hours, 'hour');
  return rtf.format(-Math.round(hours / 24), 'day');
}
