using MailCat.Emails;

namespace MailCat.Features;

/// <summary>Derives attachment features.</summary>
public sealed class AttachmentExtractor : IFeatureExtractor
{
    static readonly Dictionary<string, string> MimeByExt = new(StringComparer.OrdinalIgnoreCase)
    {
        [".pdf"] = "application/pdf", [".csv"] = "text/csv", [".ics"] = "text/calendar",
        [".xls"] = "application/vnd.ms-excel",
        [".xlsx"] = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        [".png"] = "image/png", [".jpg"] = "image/jpeg", [".jpeg"] = "image/jpeg", [".gif"] = "image/gif",
        [".txt"] = "text/plain", [".html"] = "text/html", [".zip"] = "application/zip",
        [".doc"] = "application/msword",
        [".docx"] = "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    };

    public IEnumerable<Feature> Extract(Email e)
    {
        bool pdf = false, img = false, sheet = false, cal = false;
        var types = new SortedSet<string>(StringComparer.Ordinal);
        foreach (var a in e.Attachments)
        {
            var ct = (a.ContentType ?? "").ToLowerInvariant();
            int semi = ct.IndexOf(';');
            if (semi >= 0) ct = ct[..semi];
            var ext = Path.GetExtension(a.Filename ?? "").ToLowerInvariant();
            if (ct == "" || ct == "application/octet-stream")
                ct = MimeByExt.TryGetValue(ext, out var m) ? m : "";
            if (ct == "application/pdf" || ext == ".pdf") pdf = true;
            else if (ct.StartsWith("image/")) img = true;
            else if (ct.Contains("spreadsheet") || ct.Contains("excel") || ct == "text/csv" || ext is ".csv" or ".xls" or ".xlsx") sheet = true;
            else if (ct == "text/calendar" || ext == ".ics") cal = true;
            if (ct != "") types.Add(ct);
        }
        var o = new List<Feature>
        {
            new(FeatureNames.AttachmentCount, e.Attachments.Count.ToString(), 1, FeatureSource.Parser),
            new(FeatureNames.AttachmentHasPdf, Str(pdf), 1, FeatureSource.Parser),
            new(FeatureNames.AttachmentHasImage, Str(img), 1, FeatureSource.Parser),
            new(FeatureNames.AttachmentHasSpreadsheet, Str(sheet), 1, FeatureSource.Parser),
            new(FeatureNames.AttachmentHasCalendar, Str(cal), 1, FeatureSource.Parser),
        };
        o.AddRange(types.Select(t => new Feature(FeatureNames.AttachmentTypes, t, 1, FeatureSource.Parser)));
        return o;
    }

    static string Str(bool b) => b ? "true" : "false";
}
