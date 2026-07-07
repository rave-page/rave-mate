// NOTE: not compiled/run by the Go generator - needs in-Unity verification
// (requires UdonSharp + VRChat Worlds SDK; asmdef gates on UDONSHARP).
// Poster billboard fed by a rave-mate gist (posters.json). TEXT (caption/link)
// is fully dynamic; the image is a BUILD-TIME VRCUrl slot - Udon cannot
// construct URLs at runtime, so swap image content by re-uploading to the same
// allowlisted URL. Wire sourceUrl via Tools → rave.page → World Sync.
#if UDONSHARP
using UdonSharp;
using UnityEngine;
using UnityEngine.UI;
using VRC.SDK3.Data;
using VRC.SDK3.Image;
using VRC.SDK3.StringLoading;
using VRC.SDKBase;
using VRC.Udon.Common.Interfaces;

namespace RavePage.Mate.Runtime
{
    [UdonBehaviourSyncMode(BehaviourSyncMode.None)]
    public class RaveMatePosterBoard : UdonSharpBehaviour
    {
        [Tooltip("rave-mate posters.json gist raw URL")] public VRCUrl sourceUrl;
        [Tooltip("Which poster slot this board shows")] public int posterIndex;
        [Tooltip("Re-poll seconds (min 60; gist CDN adds ~5 min)")] public float refreshSeconds = 300f;
        public Text captionText;
        public Text linkText;
        [Tooltip("Build-time image (VRC image-allowlisted host)")] public VRCUrl imageUrl;
        public RawImage image;

        VRCImageDownloader _img;

        void Start()
        {
            _Refresh();
            if (image != null && imageUrl != null && !string.IsNullOrEmpty(imageUrl.Get()))
            {
                _img = new VRCImageDownloader();
                _img.DownloadImage(imageUrl, null, (IUdonEventReceiver)this, null);
            }
        }

        public void _Refresh()
        {
            if (sourceUrl != null && !string.IsNullOrEmpty(sourceUrl.Get()))
                VRCStringDownloader.LoadUrl(sourceUrl, (IUdonEventReceiver)this);
            SendCustomEventDelayedSeconds(nameof(_Refresh), Mathf.Max(refreshSeconds, 60f));
        }

        public override void OnStringLoadSuccess(IVRCStringDownload result)
        {
            if (!VRCJson.TryDeserializeFromJson(result.Result, out DataToken root)) return;
            if (root.TokenType != TokenType.DataDictionary) return;
            if (!root.DataDictionary.TryGetValue("posters", out DataToken posters)) return;
            if (posters.TokenType != TokenType.DataList || posterIndex >= posters.DataList.Count) return;
            DataToken p = posters.DataList[posterIndex];
            if (p.TokenType != TokenType.DataDictionary) return;
            if (captionText != null && p.DataDictionary.TryGetValue("caption", out DataToken cap) && cap.TokenType == TokenType.String)
                captionText.text = cap.String;
            if (linkText != null && p.DataDictionary.TryGetValue("link", out DataToken link) && link.TokenType == TokenType.String)
                linkText.text = link.String;
        }

        public override void OnImageLoadSuccess(IVRCImageDownload result)
        {
            if (image != null) image.texture = result.Result;
        }
    }
}
#endif
