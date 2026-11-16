using System;
using System.Drawing;
using System.IO;
using System.Runtime.InteropServices;
using System.Threading.Tasks;
using System.Web.Script.Serialization;
using System.Windows.Forms;
using Microsoft.Web.WebView2.Core;
using Microsoft.Web.WebView2.WinForms;

namespace Xueer.Speaker
{
    // PanelForm is the speaker panel in its own window, done the way
    // Husic.exe does it (desktop/DesktopChrome.cs there): Windows keeps the
    // frame (snap, resize, shortcuts) but its painted title bar is removed;
    // the page draws the minimise / maximise / close buttons and its top bar
    // drags the window. No browser scrollbar either. Its profile keeps the
    // gate login. Closing only hides it; the tray stays.
    internal sealed class PanelForm : Form
    {
        private readonly WebView2 view = new WebView2();
        private readonly string url;
        private bool started;

        [StructLayout(LayoutKind.Sequential)] private struct Margins { public int Left, Right, Top, Bottom; }
        [StructLayout(LayoutKind.Sequential)] private struct Rect { public int Left, Top, Right, Bottom; }
        [StructLayout(LayoutKind.Sequential)] private struct NcCalcSize { public Rect First, Second, Third; public IntPtr Pos; }
        [DllImport("dwmapi.dll")] private static extern int DwmSetWindowAttribute(IntPtr hwnd, int attribute, ref int value, int size);
        [DllImport("dwmapi.dll")] private static extern int DwmExtendFrameIntoClientArea(IntPtr hwnd, ref Margins margins);
        [DllImport("user32.dll")] private static extern bool ReleaseCapture();
        [DllImport("user32.dll")] private static extern IntPtr SendMessage(IntPtr hwnd, int msg, IntPtr w, IntPtr l);
        [DllImport("user32.dll")] private static extern bool GetCursorPos(out Point p);
        [DllImport("user32.dll")] private static extern bool IsZoomed(IntPtr hwnd);
        [DllImport("user32.dll")] private static extern int GetSystemMetrics(int index);

        internal PanelForm(string url)
        {
            this.url = url;
            Text = "雪儿 speaker";
            Icon = new Icon(new MemoryStream(Resources.Bytes("Xueer.App.ico"))); // same as the exe and the desktop shortcut
            StartPosition = FormStartPosition.CenterScreen;
            Rectangle area = Screen.PrimaryScreen.WorkingArea;
            ClientSize = new Size(Math.Min(1180, area.Width - 80), Math.Min(820, area.Height - 80));
            MinimumSize = new Size(420, 560);
            view.Dock = DockStyle.Fill;
            Controls.Add(view);
            ApplyTheme();
        }

        private static bool AppsDark()
        {
            try
            {
                using (Microsoft.Win32.RegistryKey k = Microsoft.Win32.Registry.CurrentUser.OpenSubKey(@"Software\Microsoft\Windows\CurrentVersion\Themes\Personalize"))
                    return k != null && k.GetValue("AppsUseLightTheme") is int && (int)k.GetValue("AppsUseLightTheme") == 0;
            }
            catch (Exception) { return false; }
        }

        private void ApplyTheme()
        {
            BackColor = AppsDark() ? Color.FromArgb(7, 7, 8) : Color.FromArgb(236, 236, 240);
            view.DefaultBackgroundColor = BackColor;
        }

        protected override void OnHandleCreated(EventArgs e)
        {
            base.OnHandleCreated(e);
            try
            {
                int dark = AppsDark() ? 1 : 0, corner = 2;
                DwmSetWindowAttribute(Handle, 20, ref dark, sizeof(int));
                DwmSetWindowAttribute(Handle, 33, ref corner, sizeof(int));
                Margins m = new Margins { Left = 1, Right = 1, Top = 1, Bottom = 1 };
                DwmExtendFrameIntoClientArea(Handle, ref m); // keeps the shadow and the 1 px edge
            }
            catch (DllNotFoundException) { }
        }

        protected override void WndProc(ref Message m)
        {
            if (m.Msg == 0x0083 && m.WParam != IntPtr.Zero) // WM_NCCALCSIZE: no painted caption
            {
                if (IsZoomed(Handle))
                {
                    NcCalcSize size = (NcCalcSize)Marshal.PtrToStructure(m.LParam, typeof(NcCalcSize));
                    int x = GetSystemMetrics(32) + GetSystemMetrics(92), y = GetSystemMetrics(33) + GetSystemMetrics(92);
                    size.First.Left += x; size.First.Right -= x; size.First.Top += y; size.First.Bottom -= y;
                    Marshal.StructureToPtr(size, m.LParam, false);
                }
                m.Result = IntPtr.Zero;
                return;
            }
            base.WndProc(ref m);
        }

        internal void Reveal()
        {
            if (!Visible) Show();
            if (WindowState == FormWindowState.Minimized) WindowState = FormWindowState.Normal;
            Activate();
            if (!started) { started = true; BeginInvoke((Action)async delegate { await Start(); }); }
        }

        private async Task Start()
        {
            try
            {
                CoreWebView2Environment env = await CoreWebView2Environment.CreateAsync(null, Path.Combine(Program.Dir, "panel-profile"));
                await view.EnsureCoreWebView2Async(env);
                CoreWebView2 core = view.CoreWebView2;
                core.Settings.AreDevToolsEnabled = false;
                core.Settings.IsStatusBarEnabled = false;
                core.Settings.AreDefaultContextMenusEnabled = false;
                core.Settings.IsZoomControlEnabled = false;
                core.Settings.IsNonClientRegionSupportEnabled = false;
                core.PermissionRequested += delegate(object s, CoreWebView2PermissionRequestedEventArgs e) { e.State = CoreWebView2PermissionState.Deny; };
                core.NewWindowRequested += delegate(object s, CoreWebView2NewWindowRequestedEventArgs e)
                {
                    e.Handled = true;
                    if (e.IsUserInitiated) System.Diagnostics.Process.Start(e.Uri);
                };
                core.WebMessageReceived += Message;
                await core.AddScriptToExecuteOnDocumentCreatedAsync(Chrome.Replace("__ORIGIN__", Program.Base.TrimEnd('/').Replace("'", "")));
                core.Navigate(url);
            }
            catch (WebView2RuntimeNotFoundException)
            {
                MessageBox.Show("这台电脑还没有 WebView2，装上 Microsoft Edge WebView2 后就能打开面板。", "雪儿 speaker");
                started = false;
            }
        }

        private void Message(object sender, CoreWebView2WebMessageReceivedEventArgs e)
        {
            string action;
            try { action = new JavaScriptSerializer().Deserialize<System.Collections.Generic.Dictionary<string, object>>(e.WebMessageAsJson)["window"] as string; }
            catch (Exception) { return; }
            int hit = action == "drag" ? 2 : action == "resize-left" ? 10 : action == "resize-right" ? 11 : action == "resize-top" ? 12
                : action == "resize-top-left" ? 13 : action == "resize-top-right" ? 14 : action == "resize-bottom" ? 15
                : action == "resize-bottom-left" ? 16 : action == "resize-bottom-right" ? 17 : 0;
            if (hit != 0)
            {
                if (hit != 2 && WindowState != FormWindowState.Normal) return;
                Point p;
                if (!GetCursorPos(out p)) return;
                ReleaseCapture();
                SendMessage(Handle, 0x00A1, (IntPtr)hit, (IntPtr)((p.X & 0xffff) | ((p.Y & 0xffff) << 16))); // WM_NCLBUTTONDOWN
                return;
            }
            switch (action)
            {
                case "minimize": WindowState = FormWindowState.Minimized; break;
                case "maximize": WindowState = WindowState == FormWindowState.Maximized ? FormWindowState.Normal : FormWindowState.Maximized; break;
                case "close": Hide(); break;
            }
        }

        protected override void OnFormClosing(FormClosingEventArgs e)
        {
            if (e.CloseReason == CloseReason.UserClosing) { e.Cancel = true; Hide(); return; }
            base.OnFormClosing(e);
        }

        // Chrome is injected into the panel page (only on the configured
        // server, only inside this window): window buttons like Husic's, the page's
        // top bar drags the window, edges resize it, no scrollbar.
        private const string Chrome = @"(() => {
  if (window !== window.top || !window.chrome || !window.chrome.webview || location.origin !== '__ORIGIN__') return;
  const send = (a) => window.chrome.webview.postMessage({ window: a });
  const icons = { minimize: '<path d=""M3 9.5h10""/>', maximize: '<rect x=""3.5"" y=""3.5"" width=""9"" height=""9"" rx="".6""/>', close: '<path d=""m3.5 3.5 9 9m0-9-9 9""/>' };

  function start() {
    if (document.getElementById('xs-win')) return;
    // Styles live in the panel's own panel.css under html[data-xs-window]
    // (its CSP refuses injected <style>).
    document.documentElement.dataset.xsWindow = '1';
    const box = document.createElement('div'); box.id = 'xs-win'; box.className = 'xs-win';
    box.innerHTML = ['minimize', 'maximize', 'close'].map(a => `<button type=""button"" data-a=""${a}""><svg width=""16"" height=""16"" viewBox=""0 0 16 16"" fill=""none"" stroke=""currentColor"" stroke-width=""1.25"" stroke-linecap=""round"" stroke-linejoin=""round"">${icons[a]}</svg></button>`).join('');
    box.addEventListener('click', e => { const b = e.target.closest('button'); if (b) send(b.dataset.a); });
    document.body.append(box);
    for (const e of ['left', 'right', 'top', 'bottom', 'top-left', 'top-right', 'bottom-left', 'bottom-right']) {
      const d = document.createElement('div'); d.className = 'xs-edge'; d.dataset.e = e; document.body.append(d);
    }
    document.addEventListener('pointerdown', ev => {
      if (!ev.isTrusted || ev.button !== 0 || !(ev.target instanceof Element)) return;
      const edge = ev.target.closest('.xs-edge');
      const bar = ev.target.closest('.bar');
      if (!edge && (!bar || ev.target.closest('button, a, input, select, .xs-win'))) return;
      ev.preventDefault();
      send(edge ? 'resize-' + edge.dataset.e : 'drag');
    }, true);
    document.addEventListener('dblclick', ev => {
      if (ev.target instanceof Element && ev.target.closest('.bar') && !ev.target.closest('button, .xs-win')) send('maximize');
    }, true);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true }); else start();
})();";
    }
}
