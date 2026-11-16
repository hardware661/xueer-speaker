using System;
using System.ComponentModel;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.Runtime.InteropServices;
using System.Windows.Forms;

namespace Xueer.Speaker
{
    // The tray menu, drawn like Husic's (desktop/TrayMenu.cs there): a
    // header with what plays, a row of music buttons, a volume slider, two
    // switches, open and quit. One native popup; outside clicks close it.
    internal sealed class TrayMenu : ContextMenuStrip
    {
        private readonly Action<string, int> onCommand;
        private readonly Header header;
        private readonly Buttons buttons;
        private readonly VolumeRow volume;
        private readonly Host headerHost, buttonsHost, volumeHost;
        private readonly Item open, link, auto, quit, pair;
        private readonly ToolStripSeparator d1 = new ToolStripSeparator(), d2 = new ToolStripSeparator();
        internal Font BodyFont, TitleFont, DetailFont;
        internal Palette Colors = new Palette(false);
        internal float Zoom = 1;
        internal bool Dark;
        private bool linkPaused;

        [DllImport("user32.dll")] private static extern IntPtr MonitorFromPoint(Point point, uint flags);
        [DllImport("shcore.dll")] private static extern int GetDpiForMonitor(IntPtr monitor, int type, out uint x, out uint y);
        [DllImport("dwmapi.dll")] private static extern int DwmSetWindowAttribute(IntPtr hwnd, int attribute, ref int value, int size);

        internal TrayMenu(Action<string, int> onCommand)
        {
            this.onCommand = onCommand;
            ShowImageMargin = false;
            ShowCheckMargin = false;
            ShowItemToolTips = false;
            AutoClose = true;
            DropShadowEnabled = true;
            DoubleBuffered = true;
            Renderer = new MenuRenderer(this);
            header = new Header(this);
            buttons = new Buttons(this);
            volume = new VolumeRow(this);
            headerHost = new Host(header);
            buttonsHost = new Host(buttons);
            volumeHost = new Host(volume);
            open = MakeItem("打开面板", "open");
            link = MakeItem("暂停电脑播报", "pause-link");
            auto = MakeItem("开机自动启动", "autostart");
            pair = MakeItem("连接设置…", "pair");
            quit = MakeItem("退出", "quit");
            Items.AddRange(new ToolStripItem[] { headerHost, buttonsHost, volumeHost, d1, open, link, auto, pair, d2, quit });
            ApplyTheme();
            ApplyDpi(96);
        }

        private Item MakeItem(string text, string command)
        {
            Item item = new Item(text, command);
            item.Click += delegate
            {
                // The switches keep the menu open, like flipping a toggle.
                if (command != "pause-link" && command != "autostart") Close(ToolStripDropDownCloseReason.ItemClicked);
                onCommand(command, 0);
            };
            return item;
        }

        internal void Send(string command, int value) { onCommand(command, value); }

        internal void UpdateLink(bool connected, bool paused)
        {
            linkPaused = paused;
            link.Checked = paused;
            header.SetLink(connected, paused);
            Invalidate(true);
        }

        internal void UpdateAutoStart(bool on) { auto.Checked = on; Invalidate(true); }

        internal DateTime HoldUntil = DateTime.MinValue; // a click's state beats a stale read until then

        internal void UpdateStatus(Status s)
        {
            if (DateTime.UtcNow < HoldUntil && buttons.HasSong) s.Paused = buttons.Paused;
            header.SetSong(s.Playing, s.Artist, s.Where, s.Paused);
            buttons.SetState(!String.IsNullOrEmpty(s.Playing), s.Paused);
            if (s.Volume >= 0) volume.SetValue(s.Volume, s.PC);
            Invalidate(true);
        }

        internal int S(float v) { return (int)Math.Round(v * Zoom); }

        protected override void OnOpening(CancelEventArgs e)
        {
            ApplyTheme();
            ApplyDpi(PopupDpi());
            base.OnOpening(e);
            ApplyShape();
        }

        protected override void OnHandleCreated(EventArgs e) { base.OnHandleCreated(e); ApplyShape(); }
        protected override void OnSizeChanged(EventArgs e) { base.OnSizeChanged(e); if (IsHandleCreated) ApplyShape(); }

        // Light or dark with Windows (apps theme).
        private void ApplyTheme()
        {
            bool dark = false;
            try
            {
                using (Microsoft.Win32.RegistryKey k = Microsoft.Win32.Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize"))
                    dark = k != null && k.GetValue("AppsUseLightTheme") is int && (int)k.GetValue("AppsUseLightTheme") == 0;
            }
            catch (Exception) { }
            Dark = dark;
            Colors = new Palette(dark);
            BackColor = Colors.Background;
            ForeColor = Colors.Ink;
            header.BackColor = buttons.BackColor = volume.BackColor = Colors.Background;
        }

        private static int PopupDpi()
        {
            try
            {
                uint x, y;
                if (GetDpiForMonitor(MonitorFromPoint(Cursor.Position, 2), 0, out x, out y) == 0 && x > 0) return (int)x;
            }
            catch (DllNotFoundException) { }
            catch (EntryPointNotFoundException) { }
            using (Graphics g = Graphics.FromHwnd(IntPtr.Zero)) return (int)g.DpiX;
        }

        private void ApplyDpi(int dpi)
        {
            float next = Math.Max(72, Math.Min(384, dpi)) / 96f;
            if (BodyFont != null && Math.Abs(next - Zoom) < 0.001f) return;
            Zoom = next;
            Font a = BodyFont, b = TitleFont, c = DetailFont;
            BodyFont = new Font("Microsoft YaHei UI", 13f * Zoom, FontStyle.Regular, GraphicsUnit.Pixel);
            TitleFont = new Font("Microsoft YaHei UI", 14.667f * Zoom, FontStyle.Bold, GraphicsUnit.Pixel);
            DetailFont = new Font("Microsoft YaHei UI", 12.667f * Zoom, FontStyle.Regular, GraphicsUnit.Pixel);
            SuspendLayout();
            Font = BodyFont;
            Padding = new Padding(S(8), S(6), S(8), S(6));
            MinimumSize = MaximumSize = new Size(S(304), 0);
            headerHost.Size = header.Size = new Size(S(288), S(68));
            buttonsHost.Size = buttons.Size = new Size(S(288), S(56));
            volumeHost.Size = volume.Size = new Size(S(288), S(44));
            buttons.Arrange();
            foreach (Item i in new[] { open, link, auto, quit }) i.Size = new Size(S(288), S(44));
            foreach (ToolStripSeparator d in new[] { d1, d2 }) { d.AutoSize = false; d.Margin = Padding.Empty; d.Size = new Size(S(288), S(9)); }
            ResumeLayout(true);
            if (a != null) { a.Dispose(); b.Dispose(); c.Dispose(); }
        }

        private void ApplyShape()
        {
            if (!IsHandleCreated || Width < 2) return;
            try
            {
                int corner = 2, dark = Dark ? 1 : 0;
                int border = Colors.Border.R | (Colors.Border.G << 8) | (Colors.Border.B << 16);
                if (DwmSetWindowAttribute(Handle, 33, ref corner, sizeof(int)) != 0)
                    using (GraphicsPath p = Draw.Round(new RectangleF(0, 0, Width, Height), S(8))) Region = new Region(p);
                DwmSetWindowAttribute(Handle, 20, ref dark, sizeof(int));
                DwmSetWindowAttribute(Handle, 34, ref border, sizeof(int));
            }
            catch (DllNotFoundException) { }
            catch (EntryPointNotFoundException) { }
        }

        protected override void Dispose(bool disposing)
        {
            base.Dispose(disposing);
            if (disposing && BodyFont != null) { BodyFont.Dispose(); TitleFont.Dispose(); DetailFont.Dispose(); }
        }

        internal sealed class Palette
        {
            internal readonly Color Background, Ink, Secondary, Border, Hover, Pressed, Disabled, Track;
            internal Palette(bool dark)
            {
                Background = dark ? Color.FromArgb(36, 36, 36) : Color.FromArgb(250, 250, 250);
                Ink = dark ? Color.FromArgb(245, 245, 247) : Color.FromArgb(29, 29, 31);
                Secondary = dark ? Color.FromArgb(161, 161, 166) : Color.FromArgb(110, 110, 115);
                Border = dark ? Color.FromArgb(58, 58, 60) : Color.FromArgb(222, 222, 224);
                Hover = dark ? Color.FromArgb(54, 54, 56) : Color.FromArgb(237, 237, 238);
                Pressed = dark ? Color.FromArgb(65, 65, 67) : Color.FromArgb(226, 226, 228);
                Disabled = dark ? Color.FromArgb(105, 105, 110) : Color.FromArgb(166, 166, 171);
                Track = dark ? Color.FromArgb(76, 76, 79) : Color.FromArgb(211, 211, 214);
            }
        }

        private sealed class Host : ToolStripControlHost
        {
            internal Host(Control c) : base(c) { AutoSize = false; Margin = Padding.Empty; Padding = Padding.Empty; }
            protected override void OnClick(EventArgs e) { } // clicks inside never dismiss the menu
        }

        internal sealed class Item : ToolStripMenuItem
        {
            internal readonly string Command;
            internal Item(string text, string command) : base(text) { Command = command; AutoSize = false; Margin = Padding.Empty; Padding = Padding.Empty; }
        }

        // Header: the speaker mark, what plays, where the lines go.
        private sealed class Header : Control
        {
            private readonly TrayMenu m;
            private string title = "雪儿 speaker", detail = "";
            private bool connected, paused;
            internal Header(TrayMenu m)
            {
                this.m = m;
                SetStyle(ControlStyles.UserPaint | ControlStyles.AllPaintingInWmPaint | ControlStyles.OptimizedDoubleBuffer, true);
                SetStyle(ControlStyles.Selectable, false);
            }
            internal void SetLink(bool c, bool p) { connected = c; paused = p; Invalidate(); }
            internal void SetSong(string playing, string artist, string where, bool songPaused)
            {
                if (String.IsNullOrEmpty(playing)) { title = "雪儿 speaker"; detail = ""; }
                else
                {
                    title = playing;
                    string place = where == "pc" ? "电脑" : "音箱";
                    detail = (String.IsNullOrEmpty(artist) ? "" : artist + " · ") + place + (songPaused ? " · 暂停了" : "");
                }
                Invalidate();
            }
            protected override void OnPaint(PaintEventArgs e)
            {
                Graphics g = e.Graphics;
                g.SmoothingMode = SmoothingMode.AntiAlias;
                Rectangle art = new Rectangle(m.S(10), m.S(10), m.S(48), m.S(48));
                Draw.FillRound(g, art, m.S(10), m.Colors.Ink);
                Glyphs.Grille(g, new RectangleF(art.X + m.S(8), art.Y + m.S(8), m.S(32), m.S(32)), m.Colors.Background);
                string link = paused ? "已暂停 · 播报全走音箱" : connected ? "电脑在接播报" : "正在连服务器 · 播报走音箱";
                string sub = String.IsNullOrEmpty(detail) ? link : detail;
                TextFormatFlags f = TextFormatFlags.NoPrefix | TextFormatFlags.NoPadding | TextFormatFlags.SingleLine | TextFormatFlags.EndEllipsis | TextFormatFlags.VerticalCenter;
                TextRenderer.DrawText(g, title, m.TitleFont, new Rectangle(m.S(72), m.S(12), Width - m.S(84), m.S(24)), m.Colors.Ink, f);
                TextRenderer.DrawText(g, sub, m.DetailFont, new Rectangle(m.S(72), m.S(37), Width - m.S(84), m.S(20)), m.Colors.Secondary, f);
                if (!String.IsNullOrEmpty(detail))
                {
                    // A small dot for the link: dark when the PC takes the lines.
                    float r = m.S(3.5f);
                    using (Brush b = new SolidBrush(connected && !paused ? Color.FromArgb(52, 199, 89) : m.Colors.Disabled))
                        g.FillEllipse(b, art.Right - r * 1.2f, art.Bottom - r * 1.2f, r * 2, r * 2);
                }
            }
        }

        // Buttons: stop, play/pause (filled), next — as in Husic.
        private sealed class Buttons : Panel
        {
            private readonly TrayMenu m;
            private readonly Btn[] all;
            internal bool HasSong, Paused;
            internal Buttons(TrayMenu m)
            {
                this.m = m;
                all = new[] { new Btn(m, this, "stop"), new Btn(m, this, "toggle"), new Btn(m, this, "next") };
                Controls.AddRange(all);
            }
            internal void Arrange() { for (int i = 0; i < 3; i++) all[i].Bounds = new Rectangle(m.S(60 + i * 60), m.S(4), m.S(48), m.S(48)); }
            internal void SetState(bool hasSong, bool paused)
            {
                HasSong = hasSong; Paused = paused;
                all[0].Enabled = all[2].Enabled = hasSong;
                foreach (Btn b in all) b.Invalidate();
            }
        }

        private sealed class Btn : Button
        {
            private readonly TrayMenu m;
            private readonly Buttons row;
            private readonly string what;
            private bool hover, pressed;
            internal Btn(TrayMenu m, Buttons row, string what)
            {
                this.m = m; this.row = row; this.what = what;
                FlatStyle = FlatStyle.Flat; FlatAppearance.BorderSize = 0; UseVisualStyleBackColor = false; TabStop = false;
                SetStyle(ControlStyles.UserPaint | ControlStyles.AllPaintingInWmPaint | ControlStyles.OptimizedDoubleBuffer, true);
                Click += delegate
                {
                    string cmd = what;
                    if (what == "toggle") cmd = !row.HasSong ? "play" : row.Paused ? "resume" : "pause";
                    m.HoldUntil = DateTime.UtcNow.AddSeconds(2.5);
                    if (what == "toggle" && row.HasSong) row.SetState(true, !row.Paused); // flip at once
                    if (what == "next") row.SetState(true, false); // the next song plays
                    m.Send(cmd, 0);
                };
            }
            protected override void OnMouseEnter(EventArgs e) { hover = true; Invalidate(); base.OnMouseEnter(e); }
            protected override void OnMouseLeave(EventArgs e) { hover = false; Invalidate(); base.OnMouseLeave(e); }
            protected override void OnMouseDown(MouseEventArgs e) { pressed = true; Invalidate(); base.OnMouseDown(e); }
            protected override void OnMouseUp(MouseEventArgs e) { pressed = false; Invalidate(); base.OnMouseUp(e); }
            protected override void OnPaint(PaintEventArgs e)
            {
                Graphics g = e.Graphics;
                g.Clear(m.Colors.Background);
                g.SmoothingMode = SmoothingMode.AntiAlias;
                RectangleF body = new RectangleF(m.S(2), m.S(2), Width - m.S(4), Height - m.S(4));
                if (pressed) body.Inflate(-m.S(1), -m.S(1));
                Color ink = Enabled ? m.Colors.Ink : m.Colors.Disabled;
                if (what == "toggle")
                {
                    Color fill = pressed ? m.Colors.Secondary : hover ? (m.Dark ? Color.White : Color.FromArgb(48, 48, 50)) : m.Colors.Ink;
                    Draw.FillRound(g, body, body.Height / 2, fill);
                    ink = m.Colors.Background;
                }
                else if (Enabled && (hover || pressed)) Draw.FillRound(g, body, m.S(12), pressed ? m.Colors.Pressed : m.Colors.Hover);
                string glyph = what == "toggle" ? (row.HasSong && !row.Paused ? "pause" : "play") : what;
                Glyphs.Draw(g, glyph, new RectangleF((Width - m.S(24)) / 2f, (Height - m.S(24)) / 2f, m.S(24), m.S(24)), ink);
            }
        }

        // VolumeRow: a speaker glyph, a slider, the number. While the PC is
        // on it is the PC's system volume, otherwise the speaker's.
        private sealed class VolumeRow : Control
        {
            private readonly TrayMenu m;
            private int value = -1;
            private bool dragging, pc;
            private readonly Timer settle = new Timer { Interval = 150 };
            internal VolumeRow(TrayMenu m)
            {
                this.m = m;
                SetStyle(ControlStyles.UserPaint | ControlStyles.AllPaintingInWmPaint | ControlStyles.OptimizedDoubleBuffer, true);
                settle.Tick += delegate { settle.Stop(); if (value >= 0) m.Send("volume", value); };
            }
            internal void SetValue(int v, bool onPC) { pc = onPC; if (!dragging && !settle.Enabled) { value = v; Invalidate(); } }
            private RectangleF Track { get { return new RectangleF(m.S(44), Height / 2f - m.S(3), Width - m.S(44 + 52), m.S(6)); } }
            private void SetFromX(int x)
            {
                RectangleF t = Track;
                value = (int)Math.Round(Math.Max(0, Math.Min(1, (x - t.X) / t.Width)) * 100);
                settle.Stop(); settle.Start();
                Invalidate();
            }
            protected override void OnMouseDown(MouseEventArgs e) { if (value < 0) return; dragging = true; Capture = true; SetFromX(e.X); }
            protected override void OnMouseMove(MouseEventArgs e) { if (dragging) SetFromX(e.X); }
            protected override void OnMouseUp(MouseEventArgs e) { dragging = false; Capture = false; }
            protected override void OnMouseWheel(MouseEventArgs e)
            {
                if (value < 0) return;
                value = Math.Max(0, Math.Min(100, value + (e.Delta > 0 ? 2 : -2)));
                settle.Stop(); settle.Start(); Invalidate();
            }
            protected override void OnPaint(PaintEventArgs e)
            {
                Graphics g = e.Graphics;
                g.Clear(m.Colors.Background);
                g.SmoothingMode = SmoothingMode.AntiAlias;
                Glyphs.Draw(g, "volume", new RectangleF(m.S(14), (Height - m.S(20)) / 2f, m.S(20), m.S(20)), m.Colors.Secondary);
                RectangleF t = Track;
                Draw.FillRound(g, t, t.Height / 2, m.Colors.Track);
                if (value >= 0)
                {
                    float x = t.X + t.Width * value / 100f;
                    Draw.FillRound(g, new RectangleF(t.X, t.Y, x - t.X, t.Height), t.Height / 2, m.Colors.Ink);
                    float k = m.S(16);
                    RectangleF knob = new RectangleF(x - k / 2, Height / 2f - k / 2, k, k);
                    using (Brush shadow = new SolidBrush(Color.FromArgb(40, 0, 0, 0))) g.FillEllipse(shadow, knob.X, knob.Y + m.S(1), k, k);
                    using (Brush b = new SolidBrush(m.Dark ? Color.FromArgb(235, 235, 237) : Color.White)) g.FillEllipse(b, knob);
                    using (Pen p = new Pen(m.Colors.Border, 1)) g.DrawEllipse(p, knob);
                }
                string label = value < 0 ? "–" : value.ToString();
                TextRenderer.DrawText(g, label, m.DetailFont, new Rectangle(Width - m.S(46), 0, m.S(34), Height), m.Colors.Secondary,
                    TextFormatFlags.Right | TextFormatFlags.VerticalCenter | TextFormatFlags.NoPadding);
            }
            protected override void Dispose(bool disposing) { if (disposing) settle.Dispose(); base.Dispose(disposing); }
        }

        private sealed class MenuRenderer : ToolStripRenderer
        {
            private readonly TrayMenu m;
            internal MenuRenderer(TrayMenu m) { this.m = m; }
            protected override void OnRenderToolStripBackground(ToolStripRenderEventArgs e) { e.Graphics.Clear(m.Colors.Background); }
            protected override void OnRenderToolStripBorder(ToolStripRenderEventArgs e)
            {
                e.Graphics.SmoothingMode = SmoothingMode.AntiAlias;
                using (GraphicsPath p = Draw.Round(new RectangleF(0.5f, 0.5f, e.ToolStrip.Width - 1, e.ToolStrip.Height - 1), m.S(8)))
                using (Pen pen = new Pen(m.Colors.Border, 1)) e.Graphics.DrawPath(pen, p);
            }
            protected override void OnRenderImageMargin(ToolStripRenderEventArgs e) { }
            protected override void OnRenderItemCheck(ToolStripItemImageRenderEventArgs e) { }
            protected override void OnRenderMenuItemBackground(ToolStripItemRenderEventArgs e)
            {
                Item item = e.Item as Item;
                if (item == null) return;
                Graphics g = e.Graphics;
                g.SmoothingMode = SmoothingMode.AntiAlias;
                if (item.Selected) Draw.FillRound(g, new RectangleF(0, m.S(2), item.Width, item.Height - m.S(4)), m.S(8), item.Pressed ? m.Colors.Pressed : m.Colors.Hover);
                Glyphs.Draw(g, item.Command, new RectangleF(m.S(12), (item.Height - m.S(20)) / 2f, m.S(20), m.S(20)), m.Colors.Secondary);
                if (item.Command == "pause-link" || item.Command == "autostart")
                {
                    bool on = item.Checked;
                    RectangleF track = new RectangleF(item.Width - m.S(40), (item.Height - m.S(16)) / 2f, m.S(28), m.S(16));
                    Draw.FillRound(g, track, track.Height / 2, on ? m.Colors.Ink : m.Colors.Track);
                    RectangleF knob = new RectangleF(track.X + m.S(on ? 14 : 2), track.Y + m.S(2), m.S(12), m.S(12));
                    Draw.FillRound(g, knob, knob.Height / 2, m.Colors.Background);
                }
            }
            protected override void OnRenderItemText(ToolStripItemTextRenderEventArgs e)
            {
                Item item = e.Item as Item;
                if (item == null) { base.OnRenderItemText(e); return; }
                bool sw = item.Command == "pause-link" || item.Command == "autostart";
                Rectangle bounds = new Rectangle(m.S(44), 0, item.Width - m.S(sw ? 94 : 56), item.Height);
                TextRenderer.DrawText(e.Graphics, item.Text, m.BodyFont, bounds, m.Colors.Ink,
                    TextFormatFlags.NoPrefix | TextFormatFlags.NoPadding | TextFormatFlags.SingleLine | TextFormatFlags.VerticalCenter | TextFormatFlags.EndEllipsis);
            }
            protected override void OnRenderSeparator(ToolStripSeparatorRenderEventArgs e)
            {
                using (Pen pen = new Pen(m.Colors.Border, 1)) e.Graphics.DrawLine(pen, m.S(12), e.Item.Height / 2, e.Item.Width - m.S(12), e.Item.Height / 2);
            }
        }
    }

    internal static class Draw
    {
        internal static GraphicsPath Round(RectangleF r, float radius)
        {
            GraphicsPath path = new GraphicsPath();
            float d = Math.Min(radius * 2, Math.Min(r.Width, r.Height));
            if (d <= 0) { path.AddRectangle(r); return path; }
            path.AddArc(r.X, r.Y, d, d, 180, 90);
            path.AddArc(r.Right - d, r.Y, d, d, 270, 90);
            path.AddArc(r.Right - d, r.Bottom - d, d, d, 0, 90);
            path.AddArc(r.X, r.Bottom - d, d, d, 90, 90);
            path.CloseFigure();
            return path;
        }
        internal static void FillRound(Graphics g, RectangleF r, float radius, Color c)
        {
            using (GraphicsPath p = Round(r, radius)) using (Brush b = new SolidBrush(c)) g.FillPath(b, p);
        }
    }

    internal static class Glyphs
    {
        // Grille: the speaker's mark — a rim, a ring of eight holes, a centre.
        internal static void Grille(Graphics g, RectangleF b, Color c)
        {
            GraphicsState s = g.Save();
            g.TranslateTransform(b.X, b.Y);
            g.ScaleTransform(b.Width / 32f, b.Height / 32f);
            g.SmoothingMode = SmoothingMode.AntiAlias;
            using (Pen rim = new Pen(c, 2.4f)) g.DrawEllipse(rim, 2.2f, 2.2f, 27.6f, 27.6f);
            using (Brush br = new SolidBrush(c))
            {
                g.FillEllipse(br, 12.8f, 12.8f, 6.4f, 6.4f);
                for (int k = 0; k < 8; k++)
                {
                    double t = k * Math.PI / 4;
                    g.FillEllipse(br, 16 + 8 * (float)Math.Cos(t) - 1.9f, 16 + 8 * (float)Math.Sin(t) - 1.9f, 3.8f, 3.8f);
                }
            }
            g.Restore(s);
        }

        // TrayIcon: a white rounded tile with the black grille, like the app
        // icon; grey grille when the PC is not
        // taking the lines (not connected, or paused).
        internal static Icon TrayIcon(bool on)
        {
            int n = SystemInformation.SmallIconSize.Width * 2;
            using (Bitmap bmp = new Bitmap(n, n))
            {
                using (Graphics g = Graphics.FromImage(bmp))
                {
                    g.Clear(Color.Transparent);
                    g.SmoothingMode = SmoothingMode.AntiAlias;
                    float m = n * 0.03f;
                    RectangleF tile = new RectangleF(m, m, n - 2 * m, n - 2 * m);
                    Xueer.Speaker.Draw.FillRound(g, tile, n * 0.22f, Color.White);
                    using (GraphicsPath p = Xueer.Speaker.Draw.Round(tile, n * 0.22f))
                    using (Pen rim = new Pen(Color.FromArgb(60, 0, 0, 0), Math.Max(1f, n / 32f))) g.DrawPath(rim, p);
                    float pad = n * 0.16f;
                    Grille(g, new RectangleF(pad, pad, n - 2 * pad, n - 2 * pad), on ? Color.FromArgb(29, 29, 31) : Color.FromArgb(160, 160, 166));
                }
                IntPtr h = bmp.GetHicon();
                try { return (Icon)Icon.FromHandle(h).Clone(); }
                finally { DestroyIcon(h); }
            }
        }

        [DllImport("user32.dll")] private static extern bool DestroyIcon(IntPtr handle);

        internal static void Draw(Graphics g, string name, RectangleF b, Color color)
        {
            GraphicsState s = g.Save();
            g.TranslateTransform(b.X, b.Y);
            g.ScaleTransform(b.Width / 24f, b.Height / 24f);
            g.SmoothingMode = SmoothingMode.AntiAlias;
            using (Pen pen = new Pen(color, 1.65f))
            using (Brush brush = new SolidBrush(color))
            {
                pen.StartCap = pen.EndCap = LineCap.Round;
                pen.LineJoin = LineJoin.Round;
                switch (name)
                {
                    case "play": g.FillPolygon(brush, new[] { new PointF(9, 5), new PointF(19, 12), new PointF(9, 19) }); break;
                    case "pause":
                        Xueer.Speaker.Draw.FillRound(g, new RectangleF(7.5f, 5.5f, 3.2f, 13), 1, color);
                        Xueer.Speaker.Draw.FillRound(g, new RectangleF(13.3f, 5.5f, 3.2f, 13), 1, color);
                        break;
                    case "stop": Xueer.Speaker.Draw.FillRound(g, new RectangleF(7, 7, 10, 10), 2, color); break;
                    case "next":
                        g.DrawLine(pen, 18, 6, 18, 18);
                        g.FillPolygon(brush, new[] { new PointF(7, 5.5f), new PointF(17, 12), new PointF(7, 18.5f) });
                        break;
                    case "volume":
                        g.FillPolygon(brush, new[] { new PointF(4, 9.5f), new PointF(8, 9.5f), new PointF(12.5f, 5.5f), new PointF(12.5f, 18.5f), new PointF(8, 14.5f), new PointF(4, 14.5f) });
                        g.DrawArc(pen, 11, 8, 8, 8, -55, 110);
                        g.DrawArc(pen, 9, 4.5f, 14, 15, -50, 100);
                        break;
                    case "open":
                        using (GraphicsPath w = Xueer.Speaker.Draw.Round(new RectangleF(3, 4, 18, 16), 2.5f)) g.DrawPath(pen, w);
                        g.DrawLine(pen, 3, 9, 21, 9);
                        g.DrawLine(pen, 8, 9, 8, 20);
                        break;
                    case "pause-link": // a speaker with a slash: lines go to the speaker
                        g.DrawEllipse(pen, 5, 5, 14, 14);
                        g.FillEllipse(brush, 10.2f, 10.2f, 3.6f, 3.6f);
                        g.DrawLine(pen, 4, 20, 20, 4);
                        break;
                    case "autostart": // power
                        g.DrawArc(pen, 4.5f, 4.5f, 15, 15, -60, 300);
                        g.DrawLine(pen, 12, 3, 12, 11);
                        break;
                    case "quit":
                        g.DrawLine(pen, 7, 7, 17, 17);
                        g.DrawLine(pen, 17, 7, 7, 17);
                        break;
                }
            }
            g.Restore(s);
        }
    }
}
