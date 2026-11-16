using System;
using System.Drawing;
using System.Drawing.Drawing2D;
using System.IO;
using System.Runtime.InteropServices;
using System.Windows.Forms;

namespace Xueer.Speaker
{
    // PairForm asks for the server address and the PC key on the first run
    // (and from the tray's 连接设置…). It is drawn like the panel and the tray
    // menu: one rounded card, black-on-light (or light-on-dark), Windows 11
    // corners and shadow, sized for the monitor's DPI.
    internal sealed class PairForm : Form
    {
        private readonly TrayMenu.Palette colors;
        private readonly bool dark;
        private readonly float zoom;
        private readonly Font titleFont, bodyFont, smallFont, monoFont;
        private readonly Field url, key;
        private readonly Pill save, cancel;
        private string error = "";
        private Point dragFrom;

        internal string Url { get { return url.Box.Text.Trim().TrimEnd('/'); } }
        internal string Key { get { return key.Box.Text.Trim(); } }

        [DllImport("dwmapi.dll")] private static extern int DwmSetWindowAttribute(IntPtr hwnd, int attribute, ref int value, int size);

        internal PairForm(string current)
        {
            dark = AppsDark();
            colors = new TrayMenu.Palette(dark);
            using (Graphics g = Graphics.FromHwnd(IntPtr.Zero)) zoom = Math.Max(1f, g.DpiX / 96f);
            titleFont = new Font("Microsoft YaHei UI", 17f * zoom, FontStyle.Bold, GraphicsUnit.Pixel);
            bodyFont = new Font("Microsoft YaHei UI", 13.5f * zoom, FontStyle.Regular, GraphicsUnit.Pixel);
            smallFont = new Font("Microsoft YaHei UI", 12f * zoom, FontStyle.Regular, GraphicsUnit.Pixel);
            monoFont = new Font("Consolas", 13.5f * zoom, FontStyle.Regular, GraphicsUnit.Pixel);

            Text = "连接雪儿 speaker";
            FormBorderStyle = FormBorderStyle.None;
            StartPosition = FormStartPosition.CenterScreen;
            ShowInTaskbar = true;
            KeyPreview = true;
            DoubleBuffered = true;
            BackColor = colors.Background;
            ForeColor = colors.Ink;
            Font = bodyFont;
            ClientSize = new Size(S(400), S(300));
            try { Icon = new Icon(new MemoryStream(Resources.Bytes("Xueer.App.ico"))); } catch (Exception) { }

            url = new Field(this, "服务器地址", current.Length > 0 ? current : "http://", false) { Bounds = new Rectangle(S(28), S(92), S(344), S(54)) };
            key = new Field(this, "电脑钥匙", "", true) { Bounds = new Rectangle(S(28), S(156), S(344), S(54)) };
            cancel = new Pill(this, "取消", false) { Bounds = new Rectangle(S(28), S(236), S(166), S(40)) };
            save = new Pill(this, "连接", true) { Bounds = new Rectangle(S(206), S(236), S(166), S(40)) };
            cancel.Click += delegate { DialogResult = DialogResult.Cancel; };
            save.Click += delegate { Submit(); };
            Controls.AddRange(new Control[] { url, key, cancel, save });
            url.Box.TextChanged += delegate { SetError(""); };
            key.Box.TextChanged += delegate { SetError(""); };
            Shown += delegate { (current.Length > 0 ? key.Box : url.Box).Focus(); if (current.Length == 0) url.Box.SelectionStart = url.Box.Text.Length; };
        }

        private int S(float v) { return (int)Math.Round(v * zoom); }

        private static bool AppsDark()
        {
            try
            {
                using (Microsoft.Win32.RegistryKey k = Microsoft.Win32.Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize"))
                    return k != null && k.GetValue("AppsUseLightTheme") is int && (int)k.GetValue("AppsUseLightTheme") == 0;
            }
            catch (Exception) { return false; }
        }

        protected override void OnHandleCreated(EventArgs e)
        {
            base.OnHandleCreated(e);
            try
            {
                int corner = 2, d = dark ? 1 : 0;
                DwmSetWindowAttribute(Handle, 33, ref corner, sizeof(int)); // rounded corners + shadow on Windows 11
                DwmSetWindowAttribute(Handle, 20, ref d, sizeof(int));
            }
            catch (DllNotFoundException) { }
        }

        private void Submit()
        {
            Uri parsed;
            if (!Uri.TryCreate(Url, UriKind.Absolute, out parsed) || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host.Length == 0)
            { SetError("地址要以 http:// 或 https:// 开头"); url.Box.Focus(); return; }
            if (Key.Length < 16 || Key.Contains(" ")) { SetError("钥匙不完整：在面板「接入」里点「复制」再粘贴"); key.Box.Focus(); return; }
            DialogResult = DialogResult.OK;
        }

        private void SetError(string e) { if (error == e) return; error = e; Invalidate(new Rectangle(0, S(210), ClientSize.Width, S(26))); }

        protected override void OnKeyDown(KeyEventArgs e)
        {
            if (e.KeyCode == Keys.Escape) { DialogResult = DialogResult.Cancel; e.Handled = true; }
            else if (e.KeyCode == Keys.Enter) { Submit(); e.Handled = true; e.SuppressKeyPress = true; }
            base.OnKeyDown(e);
        }

        // The card has no title bar: drag it anywhere outside the fields.
        protected override void OnMouseDown(MouseEventArgs e) { if (e.Button == MouseButtons.Left) dragFrom = e.Location; base.OnMouseDown(e); }
        protected override void OnMouseMove(MouseEventArgs e)
        {
            if (e.Button == MouseButtons.Left) Location = new Point(Location.X + e.X - dragFrom.X, Location.Y + e.Y - dragFrom.Y);
            base.OnMouseMove(e);
        }

        protected override void OnPaint(PaintEventArgs e)
        {
            Graphics g = e.Graphics;
            g.SmoothingMode = SmoothingMode.AntiAlias;
            g.TextRenderingHint = System.Drawing.Text.TextRenderingHint.ClearTypeGridFit;
            // App mark: the grille on a tile, like the tray icon.
            RectangleF tile = new RectangleF(S(28), S(28), S(36), S(36));
            Draw.FillRound(g, tile, S(9), dark ? Color.FromArgb(58, 58, 60) : Color.White);
            using (Pen p = new Pen(colors.Border, 1)) using (GraphicsPath gp = Draw.Round(tile, S(9))) g.DrawPath(p, gp);
            Glyphs.Grille(g, RectangleF.Inflate(tile, -S(7), -S(7)), colors.Ink);
            TextFormatFlags left = TextFormatFlags.Left | TextFormatFlags.VerticalCenter | TextFormatFlags.NoPadding;
            TextRenderer.DrawText(g, "连接雪儿 speaker", titleFont, new Rectangle(S(76), S(26), S(300), S(22)), colors.Ink, left);
            TextRenderer.DrawText(g, "在网页面板「设置 → 接入」里复制", smallFont, new Rectangle(S(76), S(49), S(300), S(18)), colors.Secondary, left);
            if (error.Length > 0)
                TextRenderer.DrawText(g, error, smallFont, new Rectangle(S(30), S(212), S(344), S(20)), Color.FromArgb(255, 59, 48), left | TextFormatFlags.EndEllipsis);
            using (Pen edge = new Pen(colors.Border, 1)) g.DrawRectangle(edge, 0, 0, ClientSize.Width - 1, ClientSize.Height - 1);
        }

        protected override void Dispose(bool disposing)
        {
            if (disposing) { titleFont.Dispose(); bodyFont.Dispose(); smallFont.Dispose(); monoFont.Dispose(); }
            base.Dispose(disposing);
        }

        // Field: a grey rounded well with a small label and a borderless box.
        private sealed class Field : Control
        {
            private readonly PairForm f;
            private readonly string label;
            internal readonly TextBox Box;

            internal Field(PairForm f, string label, string text, bool secret)
            {
                this.f = f; this.label = label;
                SetStyle(ControlStyles.AllPaintingInWmPaint | ControlStyles.OptimizedDoubleBuffer | ControlStyles.UserPaint | ControlStyles.ResizeRedraw, true);
                Box = new TextBox { BorderStyle = BorderStyle.None, Text = text, Font = secret ? f.monoFont : f.bodyFont, UseSystemPasswordChar = secret };
                Controls.Add(Box);
                Box.GotFocus += delegate { Invalidate(); };
                Box.LostFocus += delegate { Invalidate(); };
                Cursor = Cursors.IBeam;
                Click += delegate { Box.Focus(); };
            }

            private Color Well { get { return f.dark ? Color.FromArgb(48, 48, 50) : Color.FromArgb(238, 238, 240); } }

            protected override void OnLayout(LayoutEventArgs e)
            {
                base.OnLayout(e);
                Box.BackColor = Well; Box.ForeColor = f.colors.Ink;
                Box.SetBounds(f.S(14), f.S(25), Width - f.S(28), Box.PreferredHeight);
            }

            protected override void OnPaint(PaintEventArgs e)
            {
                Graphics g = e.Graphics;
                g.SmoothingMode = SmoothingMode.AntiAlias;
                g.Clear(f.colors.Background);
                RectangleF r = new RectangleF(0.5f, 0.5f, Width - 1, Height - 1);
                Draw.FillRound(g, r, f.S(12), Well);
                if (Box.Focused) using (Pen p = new Pen(f.colors.Ink, 1.5f)) using (GraphicsPath gp = Draw.Round(RectangleF.Inflate(r, -0.5f, -0.5f), f.S(12))) g.DrawPath(p, gp);
                TextRenderer.DrawText(g, label, f.smallFont, new Rectangle(f.S(14), f.S(6), Width - f.S(28), f.S(16)), f.colors.Secondary,
                    TextFormatFlags.Left | TextFormatFlags.NoPadding);
            }
        }

        // Pill: a rounded button, filled for the main action.
        private sealed class Pill : Control
        {
            private readonly PairForm f;
            private readonly bool primary;
            private bool hover, down;

            internal Pill(PairForm f, string text, bool primary)
            {
                this.f = f; this.primary = primary; Text = text;
                SetStyle(ControlStyles.AllPaintingInWmPaint | ControlStyles.OptimizedDoubleBuffer | ControlStyles.UserPaint | ControlStyles.ResizeRedraw, true);
                Cursor = Cursors.Hand;
                TabStop = false;
            }

            protected override void OnMouseEnter(EventArgs e) { hover = true; Invalidate(); base.OnMouseEnter(e); }
            protected override void OnMouseLeave(EventArgs e) { hover = down = false; Invalidate(); base.OnMouseLeave(e); }
            protected override void OnMouseDown(MouseEventArgs e) { down = true; Invalidate(); base.OnMouseDown(e); }
            protected override void OnMouseUp(MouseEventArgs e) { down = false; Invalidate(); base.OnMouseUp(e); }

            protected override void OnPaint(PaintEventArgs e)
            {
                Graphics g = e.Graphics;
                g.SmoothingMode = SmoothingMode.AntiAlias;
                g.Clear(f.colors.Background);
                RectangleF r = new RectangleF(0.5f, 0.5f, Width - 1, Height - 1);
                Color fill, ink;
                if (primary)
                {
                    fill = down ? f.colors.Secondary : hover ? (f.dark ? Color.White : Color.FromArgb(58, 58, 60)) : f.colors.Ink;
                    ink = f.colors.Background;
                }
                else
                {
                    fill = down ? f.colors.Pressed : hover ? f.colors.Hover : (f.dark ? Color.FromArgb(48, 48, 50) : Color.FromArgb(238, 238, 240));
                    ink = f.colors.Ink;
                }
                Draw.FillRound(g, r, Height / 2f, fill);
                TextRenderer.DrawText(g, Text, primary ? new Font(f.bodyFont, FontStyle.Bold) : f.bodyFont, Rectangle.Round(r), ink,
                    TextFormatFlags.HorizontalCenter | TextFormatFlags.VerticalCenter | TextFormatFlags.NoPadding);
            }
        }
    }
}
